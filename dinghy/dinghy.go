package dinghy

import (
	"appengine"
	"appengine/datastore"
	"appengine/memcache"
	"appengine/user"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
)

type Post struct {
	Title       string
	Description string
	Lead        string
	Content     string    `datastore:",noindex"`
	ID          int64     `datastore:"-"`
	Date        time.Time
	Hidden	    bool
}

// A singleton datastore object containing a blog description
type Blog struct {
	Description string `datastore:",noindex"`
	Author      string `datastore:",noindex"`
	Title       string `datastore:",noindex"`
	Template    string `datastore:",noindex"`
	Posts       []Post `datastore:"-"`
	Admin       bool   `datastore:"-"`
}

func init() {
	// Ajax functions
	// post and init should have "login: admin" security in app.yaml
	http.HandleFunc("/load", load)
	http.HandleFunc("/post", post)
	http.HandleFunc("/init", config)
	http.HandleFunc("/list", list)
	http.HandleFunc("/flush", flush) // Flush memcache
	http.HandleFunc("/preview", preview)
	http.HandleFunc("/info", info)
	http.HandleFunc("/verify", verifyTemplate)
	http.HandleFunc("/delete", deletePost)

	// oauth
//	http.HandleFunc("/oauth2callback", callback)

	// Normal blog viewing
	http.HandleFunc("/", view)
}

func writePost(w io.Writer, b Blog) error {
	var fmap = template.FuncMap{
		"markdown": markdown,
	}
	viewTemplate := template.Must(template.New("view").Funcs(fmap).Parse(b.Template))

	if err := viewTemplate.Execute(w, b); err != nil {
		return err
	}
	return nil
}

// Preview is called from admin.html, which sends a form post that preview
// displays as a blog entry, without interacting with memcache or the datastore
func preview(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	b, err := getBlogInfo(c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	b.Posts = make([]Post, 1)
	b.Posts[0] = Post{
		Title:   r.FormValue("Title"),
		Date:    time.Now(),
		Lead:    "",
		Content: r.FormValue("Content"),
	}

	if err := writePost(w, b); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}


func view(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)

	// Trim leading slash and possible trailing slash from path
	path := strings.TrimSuffix(r.URL.Path[1:], "/")

	// Non-admins should get raw HTML from memcache if possible, and avoid
	// touching the datastore at all.
	if ! user.IsAdmin(c) {
		item, err := memcache.Get(c, "post." + path)
		if err == nil {
			w.Write(item.Value)
			return
		}
	}

	b, err := getBlogInfo(c)
	if user.IsAdmin(c) {
		b.Admin = true
	}

	if err != nil {
		if err == datastore.ErrNoSuchEntity {
			w.Write( []byte(`<html><body>This blog has not been set up. If you are the owner, you can visit the 
				<a href="/admin">administrator page</a> to get started</body></html>`) )
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
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
		b.Posts = make([]Post, 1)

		p, err := getPost(path, c)
		b.Posts[0] = p
		b.Title = p.Title
		b.Description = p.Description
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Admin users shouldn't write to memcache, as they can see hidden items.
	if user.IsAdmin(c) {
		if err := writePost(w, b); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	var buffer bytes.Buffer
	if err := writePost(&buffer, b); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	item := &memcache.Item {
	   Key: "post." + path,
	   Value: buffer.Bytes(),
	}
	memcache.Set(c, item)
	w.Write(item.Value)
}

func getPost(path string, c appengine.Context) (Post, error) {
	var id int64
	var err error
	p := Post{}

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

	return p, nil
}

// getBlogInfo is called inline from "/view", returning the results to the
// display template. Since it is called from every non-memcached request for a
// post, it first attempts to pull a gob-encoded Blog object from memcache. This
// is different from "func info", which is for an admin Ajax call, and always
// reads from the datastore.
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
	p, err := getPosts(10, 0, true, c)
	return p, err
}

func getPosts(num, start int, details bool, c appengine.Context) ([]Post, error) {
	p := make([]Post, 0, num)
	q := datastore.NewQuery("Post").Order("-Date").Limit(num)

	if ! user.IsAdmin(c) {
		q = q.Filter("Hidden =", false);
	}

	if details {
		q = q.Project("Title", "Lead", "Date")
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
	p, err := getRecentPosts(c)
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

func info(w http.ResponseWriter, r *http.Request) {
	b := Blog{}
	c := appengine.NewContext(r)
	k := datastore.NewKey(c, "Blog", "singleton", 0, nil)
	if err := datastore.Get(c, k, &b); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	j, err := json.Marshal(b)
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
		Title:       r.FormValue("Title"),
		Description: r.FormValue("Description"),
		Date:        time.Now(),
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

func verifyTemplate(w http.ResponseWriter, r *http.Request) {
	var fmap = template.FuncMap{
		"markdown": markdown,
	}

	_, err := template.New("config").Funcs(fmap).Parse( r.FormValue("Template") )
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, "success")
}

func deletePost(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	memcache.Flush(c)

	id, err := strconv.ParseInt(r.FormValue("ID"), 10, 64)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	k := datastore.NewKey(c, "Post", "", id, nil)
	if err := datastore.Delete(c, k); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, "success")
}

func config(w http.ResponseWriter, r *http.Request) {
	// First, define the default template. If this is a naked call to "/init",
	// or if the form field "Template" is blank, this template will be used
	const defaultViewTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="utf-8">
	<title>{{.Title}}</title>
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	{{if .Description}}
		<meta name="description" content="{{.Description}}">
	{{end}}
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

		img {
			margin-bottom: 5px;
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
					{{if .Admin}}
						<li><a href="/admin">Admin</a></li>
					{{end}}
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
	c   := appengine.NewContext(r)
	k   := datastore.NewKey(c, "Blog", "singleton", 0, nil)
	b   := Blog{}

	// Always clear the cached Blog singleton
	_ = memcache.Flush(c)

	// If we've received form data, assume this is an update...
	if r.FormValue("Title") != "" {
		b = Blog{
			Description: r.FormValue("Description"),
			Author: r.FormValue("Author"),
			Title: r.FormValue("Title"),
		}

		if r.FormValue("Template") == "" {
			b.Template = defaultViewTemplateHTML
		} else {
			b.Template = r.FormValue("Template")
		}

		_, err := datastore.Put(c, k, &b)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "success")
		return
	}

	// ...otherwise, init blog to defaults, including a dummy "About" post. This
	// will fail if the "Blog" kind already exists in the datastore. Before
	// continuing, we need an explicit "No such entity" error when querying the
	// datastore for the Blog singleton.
	err := datastore.Get(c, k, &b)
	if err == nil || err != datastore.ErrNoSuchEntity {
		msg := "Failed to initialize blog defaults. Make sure Blog datastore kind does not already exist"
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}

	b = Blog{
		Description: "My awesome blog",
		Author: "Blog author",
		Title: "Blog title",
		Template: defaultViewTemplateHTML,
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
