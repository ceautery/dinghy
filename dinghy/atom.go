package dinghy

import (
	"appengine"
	"appengine/datastore"
	"appengine/memcache"
	"encoding/xml"
	"net/http"
	"strconv"
	"time"
)

type RawFeed struct {
	XML  []byte    `datastore:",noindex"`
	Date time.Time `datastore:",noindex"`
}

type Feed struct {
	XMLName     xml.Name `xml:"feed"`
	Xmlns       string   `xml:"xmlns,attr"`
	Title       string   `xml:"title"`
	Id          string   `xml:"id"`
	Updated     string   `xml:"updated"`
	Links       *[]Link
	Author      *Author
	Entries     *[]Entry
}

type Entry struct {
	XMLName     xml.Name  `xml:"entry"`
	Title       string    `xml:"title"`
	Summary     string    `xml:"summary,omitempty"`
	Id          string    `xml:"id"`
	Updated     string    `xml:"updated"`
	Links       *[]Link
	Content     *Content
}

type Author struct {
	XMLName     xml.Name `xml:"author"`
	Name        string   `xml:"name"`
}

type Link struct {
	XMLName     xml.Name `xml:"link"`
	Rel         string   `xml:"rel,attr"`
	Href        string   `xml:"href,attr"`
	Title       string   `xml:"title,attr,omitempty"`
}

type Content struct {
	XMLName     xml.Name `xml:"content"`
	Type        string   `xml:"type,attr"`
	Content     string   `xml:",chardata"`
}

/*
 *  Feed returns an atom feed of the most recent 10 posts. We attempt to use
 *  memcache first, then the Feed datastore singleton, before generating one
 *  from scratch. The latter is very expensive, as it results in multiple calls
 *  to the datastore to query each post, and to convert the content markdown to
 *  escaped HTML.
 */
func feed(w http.ResponseWriter, r *http.Request) {
	c   := appengine.NewContext(r)
	raw := RawFeed {}
	now := time.Now()

	// If feed is in memcache, return it directly (new posts flush memcache)
	item, err := memcache.Get(c, "feed.atom")
	if err == nil {
		writeXML(item.Value, w)
		return
	}

	// If feed is in datastore and date >= most recent post date, return datastore feed

	l, err := lastPostDate(c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	k := datastore.NewKey(c, "Feed", "singleton", 0, nil)
	err = datastore.Get(c, k, &raw);
	if err != nil && err != datastore.ErrNoSuchEntity {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err == nil && raw.Date.After(l) {
		cacheOutput(c, raw.XML)
		writeXML(raw.XML, w)
		return
	}

	// Otherwise, generate feed from posts, write to datastore, memcache, and response writer
	host := appengine.DefaultVersionHostname(c)
	b, err := getBlogInfo(c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	f := Feed {
		Xmlns:   "http://www.w3.org/2005/Atom",
		Title:   b.Title,
		Id:      "tag:" + host + ",2013:dinghyBlog",
		Updated: now.Format("2006-01-02T15:04:05.000Z"),
		Author:  &Author{ Name: b.Author },
		Links:   &[]Link{
			Link{ Rel: "self", Href: "http://" + host + "/atom.xml" },
			Link{ Rel: "alternate", Href: "http://" + host + "/" },
		},
	}

	p, err := getRecentPosts(c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	entries := make([]Entry, len(p))
	for i := 0; i < len(p); i++ {
		idStr := strconv.FormatInt(p[i].ID, 10)
		post, _ := getPost(idStr, c)
		entries[i] = Entry{
			Title:    post.Title,
			Summary:  post.Description,
			Id:       f.Id + ".post-" + idStr,
			Updated:  post.Date.Format("2006-01-02T15:04:05.000Z"),
			Links:    &[]Link{
				Link{Rel: "alternate", Href: "http://" + host + "/" + idStr, Title: post.Title},
			},
			Content: &Content{Content: markdown(post.Lead, post.Content), Type: "html"},
		}
	}
	f.Entries = &entries

	output, err := xml.MarshalIndent(f, "", "    ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Write output to datastore
	raw.XML = output
	raw.Date = now
	k = datastore.NewKey(c, "Feed", "singleton", 0, nil)
	_, err = datastore.Put(c, k, &raw)

	// If we can't write to the datastore, stop. This will prevent clients from
	// seeing updates with the same contents but different timestamps, which
	// may cause duplicate updates in readers.
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// ...and to memcache
	cacheOutput(c, output)

	// ...and to HTTP caller
	writeXML(output, w)
}

func cacheOutput(c appengine.Context, output []byte) {
	item := &memcache.Item {
	   Key: "feed.atom",
	   Value: output,
	}
	memcache.Set(c, item)
}

func writeXML(buffer []byte, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/atom+xml")
	w.Write([]byte(xml.Header))
	w.Write(buffer)
}

func lastPostDate(c appengine.Context) (time.Time, error) {
	p := make([]Post, 0, 1)
	q := datastore.NewQuery("Post").Order("-Date").Limit(1)
	q = q.Filter("Hidden =", false);
	q = q.Project("Date")
	_, err := q.GetAll(c, &p)
	t := p[0].Date

	return t, err
}
