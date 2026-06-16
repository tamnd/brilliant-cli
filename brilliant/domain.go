// Package brilliant provides the kit Domain for brilliant-cli.
package brilliant

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/tamnd/any-cli/kit"
	"github.com/tamnd/any-cli/kit/errs"
)

func init() { kit.Register(Domain{}) }

// Domain is the brilliant.org driver.
type Domain struct{}

// Info describes the scheme, hostnames, and binary identity.
func (Domain) Info() kit.DomainInfo {
	return kit.DomainInfo{
		Scheme:  "brilliant",
		Aliases: []string{"br"},
		Hosts:   []string{Host},
		Identity: kit.Identity{
			Binary: "brilliant",
			Short:  "Read public Brilliant.org wiki articles and courses",
			Long: `brilliant reads public Brilliant.org wiki articles and courses.

It fetches the public wiki (math, science, computer science) and course
metadata from brilliant.org over HTTPS. No API key required; course
lessons and interactive problems require a Brilliant subscription.

Quick start:
  brilliant wiki                         list wiki articles (all topics)
  brilliant wiki --topic algebra         algebra articles
  brilliant article linear-algebra       full article detail
  brilliant topics                       list wiki topic categories
  brilliant courses                      list public courses
  brilliant course calculus-fundamentals course detail`,
			Site: Host,
			Repo: "https://github.com/tamnd/brilliant-cli",
		},
	}
}

// Register installs operations onto app.
func (Domain) Register(app *kit.App) {
	app.SetClient(newClient)

	kit.Handle(app, kit.OpMeta{
		Name:    "wiki",
		Group:   "articles",
		Summary: "List wiki articles, optionally filtered by topic",
		Args:    []kit.Arg{{Name: "topic", Help: "topic slug (algebra, calculus, ...)", Optional: true}},
	}, listWiki)

	kit.Handle(app, kit.OpMeta{
		Name:    "article",
		Group:   "articles",
		Summary: "Fetch a wiki article by slug",
		Single:  true,
		Args:    []kit.Arg{{Name: "slug", Help: "wiki article slug, e.g. linear-algebra"}},
	}, getArticle)

	kit.Handle(app, kit.OpMeta{
		Name:    "topics",
		Group:   "articles",
		Summary: "List wiki topic categories",
	}, listTopics)

	kit.Handle(app, kit.OpMeta{
		Name:    "courses",
		Group:   "courses",
		Summary: "List public courses on brilliant.org",
		Args:    []kit.Arg{{Name: "topic", Help: "filter by topic keyword", Optional: true}},
	}, listCourses)

	kit.Handle(app, kit.OpMeta{
		Name:    "course",
		Group:   "courses",
		Summary: "Fetch a course by slug",
		Single:  true,
		Args:    []kit.Arg{{Name: "slug", Help: "course slug, e.g. calculus-fundamentals"}},
	}, getCourse)
}

func newClient(_ context.Context, cfg kit.Config) (any, error) {
	delay := DefaultDelay
	if cfg.Rate > 0 {
		delay = cfg.Rate
	}
	timeout := 30 * time.Second
	if cfg.Timeout > 0 {
		timeout = cfg.Timeout
	}
	c := NewClient(delay, timeout)
	if cfg.UserAgent != "" {
		c.userAgent = cfg.UserAgent
	}
	if cfg.Retries > 0 {
		c.retries = cfg.Retries
	}
	return c, nil
}

// --- input types ---

type wikiIn struct {
	Topic  string  `kit:"arg" help:"topic slug (algebra, calculus, number-theory, combinatorics, probability, logic, computer-science)"`
	Limit  int     `kit:"flag,inherit" help:"max articles to return"`
	Client *Client `kit:"inject"`
}

type articleIn struct {
	Slug   string  `kit:"arg" help:"wiki article slug, e.g. linear-algebra"`
	Client *Client `kit:"inject"`
}

type topicsIn struct {
	Client *Client `kit:"inject"`
}

type coursesIn struct {
	Topic  string  `kit:"arg" help:"filter by topic keyword"`
	Limit  int     `kit:"flag,inherit" help:"max courses to return"`
	Client *Client `kit:"inject"`
}

type courseIn struct {
	Slug   string  `kit:"arg" help:"course slug, e.g. calculus-fundamentals"`
	Client *Client `kit:"inject"`
}

// --- handlers ---

func listWiki(ctx context.Context, in wikiIn, emit func(*Article) error) error {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	topics := DefaultSeedTopics
	if in.Topic != "" {
		topics = []string{in.Topic}
	}
	seen := map[string]bool{}
	count := 0
	for _, topic := range topics {
		if count >= limit {
			break
		}
		seedURL := WikiBase + topic + "/"
		links, err := in.Client.FetchWikiLinks(ctx, seedURL)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return mapErr(err)
		}
		for _, href := range links {
			if count >= limit {
				break
			}
			slug := wikiSlugFromHref(href)
			if slug == "" || seen[slug] {
				continue
			}
			seen[slug] = true
			a := &Article{
				Slug:  slug,
				Title: titleCaseSlug(slug),
				Topic: topic,
				URL:   WikiBase + slug + "/",
			}
			if err := emit(a); err != nil {
				return err
			}
			count++
		}
	}
	return nil
}

func getArticle(ctx context.Context, in articleIn, emit func(*Article) error) error {
	if in.Slug == "" {
		return errs.Usage("slug is required")
	}
	a, _, err := in.Client.FetchArticle(ctx, in.Slug)
	if err != nil {
		return mapErr(err)
	}
	return emit(a)
}

func listTopics(ctx context.Context, in topicsIn, emit func(*Topic) error) error {
	for _, t := range DefaultSeedTopics {
		topic := &Topic{
			Topic:        t,
			SeedURL:      WikiBase + t + "/",
			ArticleCount: 0,
		}
		if err := emit(topic); err != nil {
			return err
		}
	}
	return nil
}

func listCourses(ctx context.Context, in coursesIn, emit func(*Course) error) error {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	courses, err := in.Client.FetchCourses(ctx)
	if err != nil {
		return mapErr(err)
	}
	count := 0
	for i := range courses {
		if count >= limit {
			break
		}
		c := &courses[i]
		if in.Topic != "" && !strings.Contains(strings.ToLower(c.Category), strings.ToLower(in.Topic)) &&
			!strings.Contains(strings.ToLower(c.Title), strings.ToLower(in.Topic)) &&
			!strings.Contains(strings.ToLower(c.Slug), strings.ToLower(in.Topic)) {
			continue
		}
		if err := emit(c); err != nil {
			return err
		}
		count++
	}
	return nil
}

func getCourse(ctx context.Context, in courseIn, emit func(*Course) error) error {
	if in.Slug == "" {
		return errs.Usage("slug is required")
	}
	c, err := in.Client.FetchCourse(ctx, in.Slug)
	if err != nil {
		return mapErr(err)
	}
	return emit(c)
}

// mapErr converts library errors to kit error kinds with the right exit codes.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return errs.NotFound("%s", err.Error())
	}
	if errors.Is(err, ErrSubscriptionRequired) {
		return errs.NeedAuth("%s", err.Error())
	}
	return err
}
