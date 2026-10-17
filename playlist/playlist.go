// Package playlist stores the app's list of playable entries in a plain
// text file, so that it can be edited by hand as well.
//
// The file holds one entry per line, with the entry's name and URL
// separated by a tab, and an optional content type after them:
//
//	# My stations
//	radio1	http://radio1.com/1.mp3
//	jazz	http://jazz.example/stream	audio/aac
//
// Lines that are empty or start with '#' are comments, and are dropped
// when the file is written again. The content type is inferred from the
// URL's path when it is absent.
package playlist

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Entry is one playable item of the list.
type Entry struct {
	// Name labels the entry in the list.
	Name string
	// URL is what the device is asked to play.
	URL string
	// ContentType overrides the type inferred from the URL's extension,
	// as a stream whose URL ends in no extension needs: "audio/mpeg",
	// "audio/aac", "audio/ogg" and so on.
	ContentType string
}

// Playlist is a list of entries and the file it is stored in.
type Playlist struct {
	// Path is the file the list is read from and written to.
	Path string

	mu      sync.Mutex
	entries []Entry
}

// Load reads the list at path. A path that does not exist is not an
// error: the list is empty then, and Save creates the file.
func Load(path string) (*Playlist, error) {
	p := &Playlist{Path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.entries = entries
	return p, nil
}

// Parse reads entries from the text of a playlist file.
func Parse(text string) ([]Entry, error) {
	var entries []Entry
	s := bufio.NewScanner(strings.NewReader(text))
	// Entries are short, but a hand edited line can be long.
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; s.Scan(); line++ {
		raw := strings.TrimSuffix(s.Text(), "\r")
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		fields := strings.Split(raw, "\t")
		if len(fields) > 3 {
			return nil, fmt.Errorf("line %d: too many fields", line)
		}
		e := Entry{Name: strings.TrimSpace(fields[0])}
		if len(fields) > 1 {
			e.URL = strings.TrimSpace(fields[1])
		}
		if len(fields) > 2 {
			e.ContentType = strings.TrimSpace(fields[2])
		}
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		entries = append(entries, e)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// Entries returns a copy of the list's entries.
func (p *Playlist) Entries() []Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Entry(nil), p.entries...)
}

// Len returns how many entries the list holds.
func (p *Playlist) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// ErrDuplicate is returned by Add when the list already holds an entry
// with the same URL.
var ErrDuplicate = errors.New("the list holds this URL already")

// Add appends an entry and saves the list. It refuses an entry with the
// URL of one already there: a list of stations that plays the same stream
// twice is a mistake, not a feature.
func (p *Playlist) Add(e Entry) error {
	if err := e.validate(); err != nil {
		return err
	}
	p.mu.Lock()
	for _, have := range p.entries {
		if have.URL == e.URL {
			p.mu.Unlock()
			return ErrDuplicate
		}
	}
	p.entries = append(p.entries, e)
	p.mu.Unlock()
	return p.Save()
}

// Update replaces the entry at i and saves the list.
func (p *Playlist) Update(i int, e Entry) error {
	if err := e.validate(); err != nil {
		return err
	}
	p.mu.Lock()
	if i < 0 || i >= len(p.entries) {
		p.mu.Unlock()
		return fmt.Errorf("entry %d out of range", i)
	}
	p.entries[i] = e
	p.mu.Unlock()
	return p.Save()
}

// Remove takes the entry at i out and saves the list.
func (p *Playlist) Remove(i int) error {
	p.mu.Lock()
	if i < 0 || i >= len(p.entries) {
		p.mu.Unlock()
		return fmt.Errorf("entry %d out of range", i)
	}
	p.entries = append(p.entries[:i], p.entries[i+1:]...)
	p.mu.Unlock()
	return p.Save()
}

// Move puts the entry at from at the index to, and saves the list.
func (p *Playlist) Move(from, to int) error {
	p.mu.Lock()
	n := len(p.entries)
	if from < 0 || from >= n || to < 0 || to > n {
		p.mu.Unlock()
		return fmt.Errorf("cannot move entry %d to %d", from, to)
	}
	e := p.entries[from]
	rest := append(p.entries[:from:from], p.entries[from+1:]...)
	if to > from {
		to--
	}
	p.entries = append(rest[:to:to], append([]Entry{e}, rest[to:]...)...)
	p.mu.Unlock()
	return p.Save()
}

// Save writes the list to its file, replacing it atomically.
func (p *Playlist) Save() error {
	p.mu.Lock()
	text := format(p.entries)
	p.mu.Unlock()

	if p.Path == "" {
		return nil
	}
	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".playlist-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p.Path)
}

// format renders the entries as the text of a playlist file.
func format(entries []Entry) string {
	var b strings.Builder
	b.WriteString("# gocc playlist: name<TAB>url[<TAB>content type]\n")
	for _, e := range entries {
		b.WriteString(e.Name)
		b.WriteByte('\t')
		b.WriteString(e.URL)
		if e.ContentType != "" {
			b.WriteByte('\t')
			b.WriteString(e.ContentType)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// validate reports what an entry is missing or what it cannot hold.
func (e Entry) validate() error {
	if e.Name == "" {
		return errors.New("the name is empty")
	}
	if strings.ContainsAny(e.Name, "\t\n") {
		return errors.New("the name holds a tab or a newline")
	}
	if strings.ContainsAny(e.ContentType, "\t\n") {
		return errors.New("the content type holds a tab or a newline")
	}
	if e.URL == "" {
		return errors.New("the URL is empty")
	}
	if strings.ContainsAny(e.URL, "\t\n ") {
		return errors.New("the URL holds a space, a tab or a newline")
	}
	scheme, rest, ok := strings.Cut(e.URL, "://")
	if !ok || rest == "" {
		return fmt.Errorf("%q is not an absolute URL", e.URL)
	}
	switch strings.ToLower(scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("%q is not an http or https URL", e.URL)
	}
	return nil
}
