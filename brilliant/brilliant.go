// Package brilliant is the library behind the brilliant command line:
// the HTTP client, request shaping, and the typed data models for brilliant.org.
//
// brilliant.org hosts a public wiki of educational articles covering math,
// science, and computer science. The wiki is accessible without authentication.
// Course listings are also public metadata; interactive lessons require a
// subscription. This package wraps the public surfaces with a rate-limited
// client that the kit operations consume.
package brilliant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// DefaultUserAgent identifies the client to brilliant.org.
const DefaultUserAgent = "brilliant/dev (+https://github.com/tamnd/brilliant-cli)"

// Host is the site this client talks to.
const Host = "brilliant.org"

// BaseURL is the root every request is built from.
const BaseURL = "https://" + Host

// WikiBase is the wiki root path.
const WikiBase = BaseURL + "/wiki/"

// CoursesURL is the courses listing page.
const CoursesURL = BaseURL + "/courses/"

// DefaultDelay is the minimum gap between requests.
const DefaultDelay = 500 * time.Millisecond

// ErrNotFound is returned when the page does not exist (HTTP 404).
var ErrNotFound = errors.New("not found")

// ErrSubscriptionRequired is returned when content requires a Brilliant subscription.
var ErrSubscriptionRequired = errors.New("this content requires a Brilliant subscription (https://brilliant.org/premium)")

// DefaultSeedTopics is the list of wiki topic slugs to start discovery from.
var DefaultSeedTopics = []string{
	"algebra",
	"calculus",
	"number-theory",
	"combinatorics",
	"probability",
	"logic",
	"computer-science",
}

// Article is one wiki article entry.
type Article struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Topic       string   `json:"topic"`
	Difficulty  string   `json:"difficulty"`
	URL         string   `json:"url"`
	Related     []string `json:"related,omitempty"`
	ContentSnip string   `json:"content_snippet,omitempty"`
}

// Topic is a wiki topic seed entry.
type Topic struct {
	Topic        string `json:"topic"`
	SeedURL      string `json:"seed_url"`
	ArticleCount int    `json:"article_count"`
}

// Course is one course card from the courses listing or detail page.
type Course struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Level       string `json:"level"`
	URL         string `json:"url"`
}

// Client talks to brilliant.org over HTTP with rate limiting and retries.
type Client struct {
	http      *http.Client
	userAgent string
	delay     time.Duration
	retries   int
	mu        sync.Mutex
	last      time.Time
}

// NewClient returns a Client with the given delay between requests.
func NewClient(delay time.Duration, timeout time.Duration) *Client {
	if delay <= 0 {
		delay = DefaultDelay
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		http:      &http.Client{Timeout: timeout},
		userAgent: DefaultUserAgent,
		delay:     delay,
		retries:   3,
	}
}

// get fetches a URL and returns the body. It paces, retries on transient
// errors, and maps 403/404 to sentinel errors.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			wait := time.Duration(attempt) * 500 * time.Millisecond
			if wait > 5*time.Second {
				wait = 5 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		body, retry, err := c.do(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("get %s: %w", rawURL, lastErr)
}

func (c *Client) do(ctx context.Context, rawURL string) ([]byte, bool, error) {
	c.mu.Lock()
	if wait := c.delay - time.Since(c.last); wait > 0 {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(wait):
		}
		c.mu.Lock()
	}
	c.last = time.Now()
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.9")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		// ok
	case http.StatusNotFound:
		return nil, false, ErrNotFound
	case http.StatusForbidden:
		return nil, false, ErrSubscriptionRequired
	case http.StatusTooManyRequests:
		return nil, true, fmt.Errorf("http 429")
	default:
		if resp.StatusCode >= 500 {
			return nil, true, fmt.Errorf("http %d", resp.StatusCode)
		}
		return nil, false, fmt.Errorf("http %d", resp.StatusCode)
	}

	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, true, err
	}
	return b, false, nil
}

// FetchArticle fetches and parses a wiki article by slug.
// It returns the Article and a list of linked wiki slugs for discovery.
func (c *Client) FetchArticle(ctx context.Context, slug string) (*Article, []string, error) {
	rawURL := WikiBase + slug + "/"
	body, err := c.get(ctx, rawURL)
	if err != nil {
		return nil, nil, err
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", rawURL, err)
	}

	a := &Article{
		Slug:  slug,
		URL:   rawURL,
		Topic: topicFromSlug(slug),
	}
	a.Title = findText(doc, "h1")
	if a.Title == "" {
		a.Title = slug
	}
	a.Difficulty = findClassText(doc, "difficulty")
	if a.Difficulty == "" {
		a.Difficulty = findClassText(doc, "level")
	}

	// Content snippet from main body area.
	bodyText := extractBodyText(doc, 500)
	a.ContentSnip = bodyText

	// Wiki cross-links.
	var related []string
	var linkedSlugs []string
	seen := map[string]bool{}
	for _, href := range findAllHrefContaining(doc, "/wiki/") {
		s := wikiSlugFromHref(href)
		if s == "" || s == slug || seen[s] {
			continue
		}
		seen[s] = true
		related = append(related, s)
		linkedSlugs = append(linkedSlugs, href)
	}
	a.Related = related

	return a, linkedSlugs, nil
}

// FetchWikiLinks fetches a wiki topic seed page and returns linked article URLs.
func (c *Client) FetchWikiLinks(ctx context.Context, topicURL string) ([]string, error) {
	body, err := c.get(ctx, topicURL)
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", topicURL, err)
	}
	seen := map[string]bool{topicURL: true}
	var out []string
	for _, href := range findAllHrefContaining(doc, "/wiki/") {
		if !strings.HasPrefix(href, "http") {
			href = BaseURL + href
		}
		// Normalize trailing slash.
		if !strings.HasSuffix(href, "/") {
			href += "/"
		}
		if seen[href] {
			continue
		}
		seen[href] = true
		out = append(out, href)
	}
	return out, nil
}

// FetchCourses fetches the courses listing page and returns Course records.
func (c *Client) FetchCourses(ctx context.Context) ([]Course, error) {
	body, err := c.get(ctx, CoursesURL)
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse courses: %w", err)
	}
	seen := map[string]bool{}
	var courses []Course
	for _, href := range findAllHrefContaining(doc, "/courses/") {
		if href == "/courses/" || href == CoursesURL {
			continue
		}
		slug := courseSlugFromHref(href)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		courses = append(courses, Course{
			Slug:     slug,
			Title:    titleCaseSlug(slug),
			URL:      BaseURL + "/courses/" + slug + "/",
			Category: categoryFromSlug(slug),
		})
	}
	return courses, nil
}

// FetchCourse fetches a single course detail page.
func (c *Client) FetchCourse(ctx context.Context, slug string) (*Course, error) {
	rawURL := BaseURL + "/courses/" + slug + "/"
	body, err := c.get(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse course %s: %w", slug, err)
	}
	course := &Course{
		Slug:     slug,
		URL:      rawURL,
		Category: categoryFromSlug(slug),
	}
	course.Title = findMetaProp(doc, "og:title")
	if course.Title == "" {
		course.Title = findText(doc, "h1")
	}
	if course.Title == "" {
		course.Title = titleCaseSlug(slug)
	}
	course.Description = findMetaProp(doc, "og:description")
	return course, nil
}

// --- HTML parsing helpers (pure tree-walking, no CSS selectors) ---

// findText returns the concatenated text content of the first node with tag tagName.
func findText(n *html.Node, tagName string) string {
	var result string
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == tagName {
			result = strings.TrimSpace(nodeText(n))
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(n)
	return result
}

// findClassText finds the first element with a class attribute containing className
// and returns its text content.
func findClassText(n *html.Node, className string) string {
	var result string
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "class" && strings.Contains(a.Val, className) {
					result = strings.TrimSpace(nodeText(n))
					return true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(n)
	return result
}

// findMetaProp returns content of <meta property="prop" content="...">.
func findMetaProp(n *html.Node, prop string) string {
	var result string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "meta" {
			var isProp, content bool
			var val string
			for _, a := range n.Attr {
				if a.Key == "property" && a.Val == prop {
					isProp = true
				}
				if a.Key == "name" && a.Val == prop {
					isProp = true
				}
				if a.Key == "content" {
					content = true
					val = a.Val
				}
			}
			if isProp && content {
				result = val
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return result
}

// findAllHrefContaining returns all href values from <a> tags whose href
// contains substr.
func findAllHrefContaining(n *html.Node, substr string) []string {
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" && strings.Contains(a.Val, substr) {
					out = append(out, a.Val)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// nodeText returns the plain text of a node (concatenated text descendants).
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// extractBodyText pulls readable text from <main> or <article> or <body>,
// capped at maxLen chars.
func extractBodyText(n *html.Node, maxLen int) string {
	// Try main or article first.
	for _, tag := range []string{"main", "article"} {
		var found *html.Node
		var walk func(*html.Node) bool
		walk = func(n *html.Node) bool {
			if n.Type == html.ElementNode && n.Data == tag {
				found = n
				return true
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if walk(c) {
					return true
				}
			}
			return false
		}
		walk(n)
		if found != nil {
			text := strings.Join(strings.Fields(nodeText(found)), " ")
			if len(text) > maxLen {
				return text[:maxLen]
			}
			return text
		}
	}
	return ""
}

// --- URL/slug helpers ---

// wikiSlugFromHref extracts the slug from a /wiki/<slug>/ href.
func wikiSlugFromHref(href string) string {
	if !strings.Contains(href, "/wiki/") {
		return ""
	}
	// Absolute or relative.
	if strings.HasPrefix(href, "http") {
		parts := strings.SplitN(href, "/wiki/", 2)
		if len(parts) < 2 {
			return ""
		}
		href = parts[1]
	} else {
		href = strings.TrimPrefix(href, "/wiki/")
	}
	slug := strings.Trim(href, "/")
	// Remove sub-paths (only take first segment).
	if idx := strings.IndexByte(slug, '/'); idx >= 0 {
		slug = slug[:idx]
	}
	// Remove query/anchor.
	if idx := strings.IndexByte(slug, '?'); idx >= 0 {
		slug = slug[:idx]
	}
	if idx := strings.IndexByte(slug, '#'); idx >= 0 {
		slug = slug[:idx]
	}
	return slug
}

// courseSlugFromHref extracts the slug from a /courses/<slug>/ href.
func courseSlugFromHref(href string) string {
	if !strings.Contains(href, "/courses/") {
		return ""
	}
	if strings.HasPrefix(href, "http") {
		parts := strings.SplitN(href, "/courses/", 2)
		if len(parts) < 2 {
			return ""
		}
		href = parts[1]
	} else {
		href = strings.TrimPrefix(href, "/courses/")
	}
	slug := strings.Trim(href, "/")
	if idx := strings.IndexByte(slug, '/'); idx >= 0 {
		slug = slug[:idx]
	}
	if idx := strings.IndexByte(slug, '?'); idx >= 0 {
		slug = slug[:idx]
	}
	if idx := strings.IndexByte(slug, '#'); idx >= 0 {
		slug = slug[:idx]
	}
	return slug
}

// topicFromSlug infers the wiki topic from the slug using seed topics.
func topicFromSlug(slug string) string {
	for _, t := range DefaultSeedTopics {
		if strings.HasPrefix(slug, t) || slug == t {
			return t
		}
	}
	return ""
}

// categoryFromSlug infers a course category from its slug.
func categoryFromSlug(slug string) string {
	lower := strings.ToLower(slug)
	switch {
	case strings.Contains(lower, "computer") || strings.Contains(lower, "algorithm") ||
		strings.Contains(lower, "programming") || strings.Contains(lower, "python") ||
		strings.Contains(lower, "memory") || strings.Contains(lower, "logic") ||
		strings.Contains(lower, "ai") || strings.Contains(lower, "machine"):
		return "computer-science"
	case strings.Contains(lower, "calculus") || strings.Contains(lower, "algebra") ||
		strings.Contains(lower, "geometry") || strings.Contains(lower, "number") ||
		strings.Contains(lower, "combinatorics") || strings.Contains(lower, "probability"):
		return "mathematics"
	case strings.Contains(lower, "physics") || strings.Contains(lower, "chemistry") ||
		strings.Contains(lower, "biology") || strings.Contains(lower, "quantum"):
		return "science"
	}
	return "general"
}

// titleCaseSlug converts a slug like "number-theory" to "Number Theory".
func titleCaseSlug(slug string) string {
	parts := strings.Split(slug, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}
