package brilliant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func newTestClient() *Client {
	return NewClient(0, 5*time.Second)
}

func mustParseHTML(body string) *html.Node {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		panic(err)
	}
	return doc
}

func TestGet_sendsUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request carried no User-Agent")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := newTestClient()
	body, err := c.get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}

func TestGet_404returnsErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient()
	_, err := c.get(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %q, want to contain 'not found'", err)
	}
}

func TestGet_403returnsErrSubscription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := newTestClient()
	_, err := c.get(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "subscription") {
		t.Errorf("err = %q, want to contain 'subscription'", err)
	}
}

func TestGet_retryOn503(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("recovered"))
	}))
	defer srv.Close()

	c := newTestClient()
	c.retries = 3

	body, err := c.get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "recovered" {
		t.Errorf("body = %q, want recovered", body)
	}
	if hits != 2 {
		t.Errorf("want 2 hits, got %d", hits)
	}
}

func TestFetchWikiLinks_extractsLinks(t *testing.T) {
	body := `<!DOCTYPE html><html><body>
<h1>Algebra</h1>
<a href="/wiki/linear-algebra/">Linear Algebra</a>
<a href="/wiki/polynomials/">Polynomials</a>
<a href="/wiki/abstract-algebra/">Abstract Algebra</a>
<a href="/courses/algebra/">Algebra Course</a>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient()
	links, err := c.FetchWikiLinks(context.Background(), srv.URL+"/wiki/algebra/")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, l := range links {
		if strings.Contains(l, "/wiki/") {
			count++
		}
	}
	if count < 3 {
		t.Errorf("expected at least 3 wiki links, got %d (links: %v)", count, links)
	}
}

func TestParseTitle_fromH1(t *testing.T) {
	body := `<html><body><h1>Linear Algebra</h1></body></html>`
	doc := mustParseHTML(body)
	got := findText(doc, "h1")
	if got != "Linear Algebra" {
		t.Errorf("findText(h1) = %q, want 'Linear Algebra'", got)
	}
}

func TestParseMetaProp(t *testing.T) {
	body := `<html><head>
<meta property="og:title" content="Calculus Fundamentals"/>
<meta property="og:description" content="Learn calculus from the ground up."/>
</head><body></body></html>`
	doc := mustParseHTML(body)

	title := findMetaProp(doc, "og:title")
	if title != "Calculus Fundamentals" {
		t.Errorf("og:title = %q, want 'Calculus Fundamentals'", title)
	}
	desc := findMetaProp(doc, "og:description")
	if !strings.Contains(desc, "calculus") {
		t.Errorf("og:description = %q, want to contain 'calculus'", desc)
	}
}

func TestFindAllHrefContaining_wikiLinks(t *testing.T) {
	body := `<html><body>
<a href="/wiki/number-theory/">Number Theory</a>
<a href="/wiki/combinatorics/">Combinatorics</a>
<a href="/courses/algebra/">Algebra</a>
<a href="/about/">About</a>
</body></html>`
	doc := mustParseHTML(body)
	links := findAllHrefContaining(doc, "/wiki/")
	if len(links) != 2 {
		t.Errorf("expected 2 wiki links, got %d: %v", len(links), links)
	}
}

func TestWikiSlugFromHref(t *testing.T) {
	cases := []struct {
		href string
		want string
	}{
		{"/wiki/linear-algebra/", "linear-algebra"},
		{"https://brilliant.org/wiki/number-theory/", "number-theory"},
		{"/wiki/abstract-algebra/sub/", "abstract-algebra"},
		{"/wiki/", ""},
		{"/courses/algebra/", ""},
	}
	for _, tc := range cases {
		got := wikiSlugFromHref(tc.href)
		if got != tc.want {
			t.Errorf("wikiSlugFromHref(%q) = %q, want %q", tc.href, got, tc.want)
		}
	}
}

func TestCourseSlugFromHref(t *testing.T) {
	cases := []struct {
		href string
		want string
	}{
		{"/courses/calculus-fundamentals/", "calculus-fundamentals"},
		{"https://brilliant.org/courses/algorithms/", "algorithms"},
		{"/courses/", ""},
	}
	for _, tc := range cases {
		got := courseSlugFromHref(tc.href)
		if got != tc.want {
			t.Errorf("courseSlugFromHref(%q) = %q, want %q", tc.href, got, tc.want)
		}
	}
}

func TestTitleCaseSlug(t *testing.T) {
	cases := []struct{ slug, want string }{
		{"number-theory", "Number Theory"},
		{"linear-algebra", "Linear Algebra"},
		{"cs", "Cs"},
		{"computer-science-fundamentals", "Computer Science Fundamentals"},
	}
	for _, tc := range cases {
		got := titleCaseSlug(tc.slug)
		if got != tc.want {
			t.Errorf("titleCaseSlug(%q) = %q, want %q", tc.slug, got, tc.want)
		}
	}
}

func TestCategoryFromSlug(t *testing.T) {
	cases := []struct{ slug, want string }{
		{"computer-science-fundamentals", "computer-science"},
		{"algorithms", "computer-science"},
		{"calculus-fundamentals", "mathematics"},
		{"algebra", "mathematics"},
		{"physics-fundamentals", "science"},
	}
	for _, tc := range cases {
		got := categoryFromSlug(tc.slug)
		if got != tc.want {
			t.Errorf("categoryFromSlug(%q) = %q, want %q", tc.slug, got, tc.want)
		}
	}
}

func TestFetchCourses_parsesCourseCards(t *testing.T) {
	body := `<!DOCTYPE html><html><body>
<a href="/courses/algebra-fundamentals/">Algebra Fundamentals</a>
<a href="/courses/calculus-fundamentals/">Calculus</a>
<a href="/courses/computer-science-fundamentals/">CS Fundamentals</a>
<a href="/about/">About</a>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	rawBody, err := newTestClient().get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	doc := mustParseHTML(string(rawBody))
	seen := map[string]bool{}
	var courses []Course
	for _, href := range findAllHrefContaining(doc, "/courses/") {
		if href == "/courses/" {
			continue
		}
		slug := courseSlugFromHref(href)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		courses = append(courses, Course{Slug: slug})
	}
	if len(courses) != 3 {
		t.Errorf("expected 3 courses, got %d", len(courses))
	}
}

func TestExtractBodyText_fromMain(t *testing.T) {
	body := `<html><body><main><p>Linear algebra is the study of vectors.</p></main></body></html>`
	doc := mustParseHTML(body)
	text := extractBodyText(doc, 500)
	if !strings.Contains(text, "vectors") {
		t.Errorf("extractBodyText = %q, want to contain 'vectors'", text)
	}
}

func TestDefaultSeedTopics_count(t *testing.T) {
	if len(DefaultSeedTopics) < 5 {
		t.Errorf("expected at least 5 seed topics, got %d", len(DefaultSeedTopics))
	}
}

func TestNewClient_defaults(t *testing.T) {
	c := NewClient(0, 0)
	if c.delay <= 0 {
		t.Error("expected positive delay")
	}
	if c.http == nil {
		t.Error("expected non-nil HTTP client")
	}
}
