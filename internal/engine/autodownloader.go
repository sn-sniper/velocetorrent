package engine

import (
	"encoding/xml"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RSSFeedRule defines an automated RSS/Atom feed subscription with regex filtering.
type RSSFeedRule struct {
	URL         string `json:"url"`
	FilterRegex string `json:"filterRegex"`
	OutputDir   string `json:"outputDir,omitempty"`
}

type rssDocument struct {
	Items []rssItem `xml:"channel>item"`
	Entry []rssAtom `xml:"entry"`
}

type rssItem struct {
	Title     string       `xml:"title"`
	Link      string       `xml:"link"`
	GUID      string       `xml:"guid"`
	Enclosure rssEnclosure `xml:"enclosure"`
}

type rssAtom struct {
	Title string      `xml:"title"`
	ID    string      `xml:"id"`
	Link  rssAtomLink `xml:"link"`
}

type rssAtomLink struct {
	Href string `xml:"href,attr"`
}

type rssEnclosure struct {
	URL string `xml:"url,attr"`
}

// AutoDownloader watches a local directory for .torrent/.magnet files and polls RSS feeds with regex filters.
type AutoDownloader struct {
	eng           *Engine
	mu            sync.RWMutex
	watchDir      string
	feeds         []RSSFeedRule
	seenItems     map[string]struct{}
	ingestedCount atomic.Int64

	closed  atomic.Bool
	closeCh chan struct{}
	wg      sync.WaitGroup
}

// NewAutoDownloader starts background watch-folder and RSS auto-ingestion workers.
func NewAutoDownloader(eng *Engine, watchDir string, feeds []RSSFeedRule) *AutoDownloader {
	if strings.TrimSpace(watchDir) == "" {
		if home, err := os.UserHomeDir(); err == nil {
			watchDir = filepath.Join(home, "Downloads", "torrents")
		}
	}
	ad := &AutoDownloader{
		eng:       eng,
		watchDir:  strings.TrimSpace(watchDir),
		feeds:     append([]RSSFeedRule(nil), feeds...),
		seenItems: make(map[string]struct{}),
		closeCh:   make(chan struct{}),
	}
	ad.wg.Add(1)
	go ad.workerLoop()
	return ad
}

// Configure updates the watched directory and RSS feed rules at runtime.
func (a *AutoDownloader) Configure(watchDir string, feeds []RSSFeedRule) {
	a.mu.Lock()
	if strings.TrimSpace(watchDir) != "" {
		a.watchDir = strings.TrimSpace(watchDir)
	}
	if feeds != nil {
		a.feeds = append([]RSSFeedRule(nil), feeds...)
	}
	a.mu.Unlock()
	a.ScanNow()
}

// Snapshot returns current watch-folder and RSS configuration plus total ingested count.
func (a *AutoDownloader) Snapshot() (watchDir string, feeds []RSSFeedRule, ingested int64) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.watchDir, append([]RSSFeedRule(nil), a.feeds...), a.ingestedCount.Load()
}

func (a *AutoDownloader) workerLoop() {
	defer a.wg.Done()
	watchTicker := time.NewTicker(2 * time.Second)
	rssTicker := time.NewTicker(5 * time.Minute)
	defer watchTicker.Stop()
	defer rssTicker.Stop()

	for {
		select {
		case <-a.closeCh:
			return
		case <-watchTicker.C:
			a.scanWatchFolder()
		case <-rssTicker.C:
			a.pollRSSFeeds()
		}
	}
}

// ScanNow triggers an immediate watch-folder scan and RSS feed poll.
func (a *AutoDownloader) ScanNow() {
	a.scanWatchFolder()
	a.pollRSSFeeds()
}

func (a *AutoDownloader) scanWatchFolder() {
	if a.closed.Load() || a.eng == nil {
		return
	}
	a.mu.RLock()
	dir := a.watchDir
	a.mu.RUnlock()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		fullPath := filepath.Join(dir, name)

		if strings.HasSuffix(lower, ".torrent") {
			if _, err := a.eng.AddTorrentFromFile(fullPath, "", false); err == nil {
				_ = os.Remove(fullPath)
				a.ingestedCount.Add(1)
			}
		} else if strings.HasSuffix(lower, ".magnet") {
			raw, readErr := os.ReadFile(fullPath)
			if readErr == nil {
				uri := strings.TrimSpace(string(raw))
				if _, addErr := a.eng.AddTorrentFromMagnet(uri, "", false); addErr == nil {
					_ = os.Remove(fullPath)
					a.ingestedCount.Add(1)
				}
			}
		}
	}
}

func (a *AutoDownloader) pollRSSFeeds() {
	if a.closed.Load() || a.eng == nil {
		return
	}
	a.mu.RLock()
	feeds := append([]RSSFeedRule(nil), a.feeds...)
	a.mu.RUnlock()

	if len(feeds) == 0 {
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	for _, rule := range feeds {
		if strings.TrimSpace(rule.URL) == "" {
			continue
		}
		var re *regexp.Regexp
		if strings.TrimSpace(rule.FilterRegex) != "" {
			var err error
			re, err = regexp.Compile(rule.FilterRegex)
			if err != nil {
				continue
			}
		}

		resp, err := client.Get(rule.URL)
		if err != nil {
			continue
		}
		xmlBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
		_ = resp.Body.Close()
		if readErr != nil {
			continue
		}

		var doc rssDocument
		if err := xml.Unmarshal(xmlBytes, &doc); err != nil {
			continue
		}

		type candidate struct {
			id    string
			title string
			url   string
		}
		var items []candidate
		for _, it := range doc.Items {
			u := strings.TrimSpace(it.Enclosure.URL)
			if u == "" {
				u = strings.TrimSpace(it.Link)
			}
			id := strings.TrimSpace(it.GUID)
			if id == "" {
				id = u
			}
			items = append(items, candidate{id: id, title: it.Title, url: u})
		}
		for _, en := range doc.Entry {
			u := strings.TrimSpace(en.Link.Href)
			id := strings.TrimSpace(en.ID)
			if id == "" {
				id = u
			}
			items = append(items, candidate{id: id, title: en.Title, url: u})
		}

		for _, c := range items {
			if c.url == "" {
				continue
			}
			if re != nil && !re.MatchString(c.title) && !re.MatchString(c.url) {
				continue
			}
			a.mu.Lock()
			_, seen := a.seenItems[c.id]
			if !seen {
				a.seenItems[c.id] = struct{}{}
			}
			a.mu.Unlock()
			if seen {
				continue
			}

			if strings.HasPrefix(strings.ToLower(c.url), "magnet:?") {
				if _, err := a.eng.AddTorrentFromMagnet(c.url, rule.OutputDir, false); err == nil {
					a.ingestedCount.Add(1)
				}
			} else if strings.HasPrefix(strings.ToLower(c.url), "http://") || strings.HasPrefix(strings.ToLower(c.url), "https://") {
				tResp, tErr := client.Get(c.url)
				if tErr == nil {
					tBytes, rErr := io.ReadAll(io.LimitReader(tResp.Body, 8*1024*1024))
					_ = tResp.Body.Close()
					if rErr == nil {
						if _, addErr := a.eng.AddTorrentFromBytes(tBytes, rule.OutputDir, false); addErr == nil {
							a.ingestedCount.Add(1)
						}
					}
				}
			}
		}
	}
}

// Close stops the background watch-folder and RSS poller goroutine.
func (a *AutoDownloader) Close() {
	if !a.closed.CompareAndSwap(false, true) {
		return
	}
	close(a.closeCh)
	a.wg.Wait()
}
