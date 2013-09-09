package dinghy

import (
	"appengine"
	"appengine/datastore"
	"appengine/memcache"
	"appengine/user"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
)

type Post struct {
	Title   string
	Lead    string
	Content string `datastore:",noindex"`
	ID      int64  `datastore:"-"`
	Date    time.Time
	Hidden	bool
}

// A singleton datastore object containing a blog description
type Blog struct {
	Description string `datastore:",noindex"`
	Author      string `datastore:",noindex"`
	Title       string `datastore:",noindex"`
	Template    string `datastore:",noindex"`
	Posts       []Post `datastore:"-"`
}

func init() {
	// Ajax functions
	// post and init should have "login: admin" security in app.yaml
	http.HandleFunc("/load", load)
	http.HandleFunc("/post", post)
	http.HandleFunc("/init", defaults)
	http.HandleFunc("/list", list)
	http.HandleFunc("/flush", flush) // Flush memcache

	// oauth
//	http.HandleFunc("/oauth2callback", callback)

	// Normal blog viewing
	http.HandleFunc("/", view)
}

//func callback() {
// Client ID:           16812864608.apps.googleusercontent.com
// Email address:       16812864608@developer.gserviceaccount.com
// Client secret:       SgrCoX8AU6aMIrc7fY_aF-ZX
// Redirect URIs:       https://curtisautery.appspot.com/oauth2callback
// JavaScript origins:  https://curtisautery.appspot.com

// https://accounts.google.com/o/oauth2/auth
// ?state=/profile
// &redirect_uri=https://oauth2-login-demo.appspot.com/code
// &response_type=code
// &client_id=812741506391.apps.googleusercontent.com
// &approval_prompt=force
// &scope=https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile

// https://picasaweb.google.com/data/feed/api/user/ceautery
// album ID -> gphoto:id = 5551425951876431345
// 
// https://picasaweb.google.com/data/feed/api/user/ceautery/albumid/5551425951876431345
// 5845231177108333697

// Markdown, oauth, picasa

//}

func view(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	b, err := getBlogInfo(c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.URL.Path == "/" {
		// Get Leads for recent posts
		// TODO: Play with r.URL.RawQuery for custom searches
		p, err := getRecentPosts(c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b.Posts = p
	} else {
		// Trim leading slash and possible trailing slash from path
		path := strings.TrimSuffix(r.URL.Path[1:], "/")
		b.Posts = make([]Post, 1)

		p, err := getPost(path, c)
		b.Posts[0] = p
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	var fmap = template.FuncMap{
		"markdown": markdown,
	}

	viewTemplate := template.Must(template.New("view").Funcs(fmap).Parse(b.Template))

	//TODO: send template output to memcache for given path, then output to http writer

	if err := viewTemplate.Execute(w, b); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func getPost(path string, c appengine.Context) (Post, error) {
	var id int64
	var err error
	p := Post{}

	_, err = memcache.Gob.Get(c, "post." + path, &p)
	if err == nil {
		return p, nil
	}

	var k *datastore.Key

	if m, _ := regexp.MatchString("^\\d+$", path); m {
		// Numeric only, assume path is datastore ID
		id, err = strconv.ParseInt(path, 10, 64)
		if err != nil {
			return p, err
		}

		k = datastore.NewKey(c, "Post", "", id, nil)

		if err := datastore.Get(c, k, &p); err != nil {
			return p, err
		}
	} else {
		p.Title = "Page not found"
		return p, nil
	}

	if ! user.IsAdmin(c) && p.Hidden {
		return Post{ Title: "This page is currently unavailable" }, nil
	}

	if ! user.IsAdmin(c) {
		item := &memcache.Item {
		   Key: "post." + path,
		   Object: p,
		}
		memcache.Gob.Set(c, item)
	}

	return p, nil
}

func getBlogInfo(c appengine.Context) (Blog, error) {
	var err error
	b := Blog{}

	_, err = memcache.Gob.Get(c, "blog", &b)
	if err == nil {
		return b, nil
	}

	k := datastore.NewKey(c, "Blog", "singleton", 0, nil)
	if err := datastore.Get(c, k, &b); err != nil {
		return b, err
	}

	item := &memcache.Item {
	   Key: "blog",
	   Object: b,
	}
	memcache.Gob.Set(c, item)
	return b, nil
}

func getRecentPosts(c appengine.Context) ([]Post, error) {
	var err error
	p := make([]Post, 0, 10)

	_, err = memcache.Gob.Get(c, "recent", &p)
	if err == nil {
		return p, nil
	}

	p, err = getPosts(10, 0, true, c)
	if err == nil && ! user.IsAdmin(c) {
		item := &memcache.Item {
		   Key: "recent",
		   Object: p,
		}
		memcache.Gob.Set(c, item)
	}
	return p, err
}

func getPosts(num, start int, details bool, c appengine.Context) ([]Post, error) {
	p := make([]Post, 0, num)
	q := datastore.NewQuery("Post").Order("-Date").Limit(num)

	if ! user.IsAdmin(c) {
		q = q.Filter("Hidden =", false);
	}

	if details {
		// Bug in SDK 1.8.3 with dates in projection queries,
		// so we'll just grab all columns instead
		// q = q.Project("Title", "Lead", "Date")
	} else {
		q = q.Project("Title")
	}

	keys, err := q.GetAll(c, &p)
	if err != nil {
		return p, err
	}

	for i := 0; i < len(p); i++ {
		p[i].ID = keys[i].IntID()
		p[i].Content = ""
	}

	return p, nil
}

// AJAX functions
func flush(w http.ResponseWriter, r *http.Request) {
	err := memcache.Flush(appengine.NewContext(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, "success")
}

func list(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	p := make([]Post, 0, 10)
	var err error

	if r.FormValue("clean") == "" {
		p, err = getRecentPosts(c)
	} else {
		p, err = getPosts(10, 0, true, c)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	j, err := json.Marshal(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprintf(w, "%s", j)
}

func load(w http.ResponseWriter, r *http.Request) {
	p, err := getPost(r.FormValue("id"), appengine.NewContext(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	j, err := json.Marshal(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprintf(w, "%s", j)
}

func post(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	p := Post{
		Title: r.FormValue("Title"),
		Date:  time.Now(),
	}

	content := r.FormValue("Content")
	re := regexp.MustCompile( `(.*\n){5}` )
	five := re.FindStringIndex(content)
	
	var i int
	switch {
	case five != nil && five[1] < 500:
		i = five[1]
	case len(content) < 500:
		i = len(content)
	default:
		i = 500
	}

	p.Lead = content[0:i]
	p.Content = content[i:]

	if r.FormValue("Hidden") == "" {
		p.Hidden = false
	} else {
		p.Hidden = true
	}

	if r.FormValue("date") == "" {
		p.Date = time.Now()
	} else {
		date, err := time.Parse("2006-01-02T15:04:05.999Z", r.FormValue("date"))
		if err != nil {
			p.Date = time.Now()
		} else {
			p.Date = date
		}
	}

	if err := memcache.Flush(c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	k := new(datastore.Key)
	if r.FormValue("id") == "" {
		k = datastore.NewIncompleteKey(c, "Post", nil)
	} else {
		id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		k = datastore.NewKey(c, "Post", "", id, nil)
	}

	_, err := datastore.Put(c, k, &p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, "success")
}

func defaults(w http.ResponseWriter, r *http.Request) {
	c   := appengine.NewContext(r)
	k   := datastore.NewKey(c, "Blog", "singleton", 0, nil)
	b   := Blog{}
	err := datastore.Get(c, k, &b)

	if err == nil || err != datastore.ErrNoSuchEntity {
		msg := "Failed to initialize blog defaults. Make sure Blog datastore kind does not already exist"
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}

	const defaultViewTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="utf-8">
	<title>{{.Title}}</title>
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<meta name="description" content="{{.Description}}">
	<meta name="author" content="{{.Author}}">

	<link href="/static/bootstrap/css/bootstrap.min.css" rel="stylesheet">

	<style type="text/css">
		#cover {
			background-size: 100% auto;
			background-image: url(/static/cover.jpg);
			border-radius: 6px;
			margin-top: -20px;
		}

		#cover h1 {
			color: #fff;
			background: rgba(0, 0, 0, 0.4);
			line-height: 1.5;
			font-size: 2em;
		}

		@media screen and (min-width: 768px) {
			#cover h1 {
			    font-size: 63px;
			}

			#body {
				text-align: justify;
			}
		}

		hr {
			border-top: 1px solid gray;
		}
	</style>

	<script type="text/javascript">
		function populate_entries( entries ) {
			$( '#entries' ).empty();
			for ( var e in entries ) add_entry( entries[e].Title, entries[e].ID );
		}

		function add_entry( title, key ) {
			var html = '<li><a href="/' + key + '">' + title + '</a></li>';
			$( '#entries' ).append( html );
		}

		function init() {
			if (document.location.pathname == "/1") $('#About').addClass("active");
			if (document.location.pathname == "/") {
				$('#Home').addClass("active");
				$('#latest').empty();
			} else {
				$.ajax({
					url: '/list',
					type: 'GET',
					success: function( results ) {
						populate_entries( $.parseJSON( results ) );
					},
					error: function( xhr, ajaxOptions, thrownError ) {
						console.log( xhr );
						console.log( ajaxOptions );
						alert( thrownError );
					}
				});
			}
		}
	</script>
</head>
<body onload="init()">
	<div class="navbar navbar-inverse navbar-static-top">
		<div class="container">
			<div class="navbar-header">
				<button type="button" class="navbar-toggle" data-toggle="collapse" data-target=".navbar-collapse">
					<span class="icon-bar"></span>
					<span class="icon-bar"></span>
					<span class="icon-bar"></span>
				</button>
			</div>

			<div class="navbar-collapse collapse">
				<ul class="nav navbar-nav">
					<li id="Home"><a href="/">Home</a></li>
					<li id="latest" class="dropdown">
						<a href="#" class="dropdown-toggle" data-toggle="dropdown">
							Latest
							<b class="caret"></b>
						</a>
						<ul id="entries" class="dropdown-menu">
							<li class="muted"><a href="javascript:blur()">loading...</a></li>
						</ul>
					</li>
				</ul>
				<ul class="nav navbar-nav navbar-right">
					<li id="About"><a href="/1">About</a></li>
				</ul>
			</div>
		</div>
	</div>

	<div class="container col-xs-12 col-md-8 col-md-offset-2">
		<div id="cover">
			<h1 class="text-center jumbotron">{{.Title}}</h1>
		</div>

		{{range .Posts}}
			<h3 class="text-center">
				{{if .ID}}
					<a href="/{{.ID}}">{{.Title}}</a>
				{{else}}
					{{.Title}}
				{{end}}
			</h3>
			<h4>{{.Date.Format "Monday, January 02, 2006"}}</h4>
			<div id="body">
				{{markdown .Lead .Content}}
			</div>
			<hr />
		{{end}}

	</div>
	<script src="/static/jquery/jquery-2.0.3.min.js"></script>
	<script src="/static/bootstrap/js/bootstrap.min.js"></script>
</body>
</html>
`
	b = Blog{
		Description: "A lightweight blog engine using Go, GAE, Bootstrap, and jQuery",
		Author: "Blog author",
		Title: "Blog title",
		Template: defaultViewTemplateHTML,
	}

	if r.FormValue("Title") != "" {
		b.Title = r.FormValue("Title")
	}

	if r.FormValue("Author") != "" {
		b.Author = r.FormValue("Author")
	}

	_, err = datastore.Put(c, k, &b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	p := Post {
		Date:  time.Now(),
		Title: "About this blog",
		Lead:  "This is a demo blog 'About' page",
	}

	k = datastore.NewKey(c, "Post", "", 1, nil)
	_, err = datastore.Put(c, k, &p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, "success")
}
