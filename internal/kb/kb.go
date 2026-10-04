// Package kb loads the markdown knowledge base and ranks sections with BM25.
//
// Each "## " heading in a file is one chunk. Chunk IDs are stable
// ("returns#return-window") so the model can cite them and the guard layer
// can verify every citation points at text that was actually retrieved.
package kb

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// BM25 parameters (standard defaults).
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

type Chunk struct {
	ID     string
	Source string
	Title  string
	Text   string

	tf     map[string]int
	length int
}

type Hit struct {
	ChunkID string
	Title   string
	Text    string
	Score   float64
}

type Index struct {
	chunks []Chunk
	byID   map[string]int
	df     map[string]int
	avgLen float64
}

// LoadDir parses every *.md file in dir. It fails loudly on malformed files
// (no sections, empty sections, duplicate IDs) rather than silently indexing
// less content than expected.
func LoadDir(dir string) (*Index, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("kb: no markdown files in %s", dir)
	}
	sort.Strings(files)

	var all []Chunk
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		source := strings.TrimSuffix(filepath.Base(f), ".md")
		cs, err := parseFile(source, string(b))
		if err != nil {
			return nil, err
		}
		all = append(all, cs...)
	}
	return Build(all)
}

func parseFile(source, content string) ([]Chunk, error) {
	var (
		out  []Chunk
		cur  *Chunk
		body []string
	)
	flush := func() error {
		if cur == nil {
			return nil
		}
		cur.Text = strings.TrimSpace(strings.Join(body, "\n"))
		if cur.Text == "" {
			return fmt.Errorf("kb: %s: empty section %q", source, cur.Title)
		}
		out = append(out, *cur)
		return nil
	}

	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "## ") {
			if err := flush(); err != nil {
				return nil, err
			}
			title := strings.TrimSpace(line[3:])
			cur = &Chunk{ID: source + "#" + slugify(title), Source: source, Title: title}
			body = nil
			continue
		}
		if cur != nil {
			body = append(body, line)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("kb: %s: no '## ' sections", source)
	}
	return out, nil
}

// Build indexes chunks. Exposed so tests can index synthetic content.
func Build(chunks []Chunk) (*Index, error) {
	idx := &Index{byID: map[string]int{}, df: map[string]int{}}
	total := 0
	for i := range chunks {
		c := chunks[i]
		if _, dup := idx.byID[c.ID]; dup {
			return nil, fmt.Errorf("kb: duplicate chunk id %q", c.ID)
		}
		toks := tokenize(c.Title + " " + c.Text)
		c.tf = map[string]int{}
		for _, t := range toks {
			c.tf[t]++
		}
		c.length = len(toks)
		total += c.length
		for t := range c.tf {
			idx.df[t]++
		}
		idx.byID[c.ID] = len(idx.chunks)
		idx.chunks = append(idx.chunks, c)
	}
	if len(idx.chunks) > 0 {
		idx.avgLen = float64(total) / float64(len(idx.chunks))
	}
	return idx, nil
}

// Search returns up to k chunks with a positive BM25 score, best first.
func (idx *Index) Search(query string, k int) []Hit {
	q := tokenize(query)
	if len(q) == 0 || k <= 0 {
		return nil
	}
	n := float64(len(idx.chunks))
	var hits []Hit
	for _, c := range idx.chunks {
		score := 0.0
		for _, t := range q {
			f := float64(c.tf[t])
			if f == 0 {
				continue
			}
			df := float64(idx.df[t])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			norm := f * (bm25K1 + 1) / (f + bm25K1*(1-bm25B+bm25B*float64(c.length)/idx.avgLen))
			score += idf * norm
		}
		if score > 0 {
			hits = append(hits, Hit{ChunkID: c.ID, Title: c.Title, Text: c.Text, Score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// Get returns a chunk by ID.
func (idx *Index) Get(id string) (Chunk, bool) {
	i, ok := idx.byID[id]
	if !ok {
		return Chunk{}, false
	}
	return idx.chunks[i], true
}

func (idx *Index) Len() int { return len(idx.chunks) }

var (
	wordRe    = regexp.MustCompile(`[a-z0-9]+`)
	nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)
	stopwords = map[string]bool{
		"a": true, "an": true, "the": true, "is": true, "are": true, "was": true,
		"do": true, "doe": true, "i": true, "my": true, "me": true, "you": true,
		"your": true, "to": true, "of": true, "for": true, "and": true, "or": true,
		"in": true, "on": true, "at": true, "it": true, "can": true, "how": true,
		"what": true, "when": true, "with": true, "be": true, "will": true,
		"if": true, "this": true, "that": true, "we": true, "our": true,
		"have": true, "ha": true, "by": true, "from": true, "not": true,
	}
)

func tokenize(s string) []string {
	words := wordRe.FindAllString(strings.ToLower(s), -1)
	out := words[:0]
	for _, w := range words {
		w = stem(w)
		if stopwords[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// stem is intentionally crude and explainable: strip one suffix ("-ing",
// "-ed" or a plural "-s"), then collapse a trailing double consonant so
// "shipping"/"shipped"/"ship" and "covered"/"cover" meet. Added after the
// eval showed refund-timing and warranty questions missing the threshold
// (DECISIONS.md D24). Not a real stemmer: "fitting" -> "fit" works,
// "dries" -> "drie" does not.
func stem(w string) string {
	switch {
	case len(w) > 5 && strings.HasSuffix(w, "ing"):
		w = w[:len(w)-3]
	case len(w) > 4 && strings.HasSuffix(w, "ed"):
		w = w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		w = w[:len(w)-1]
	}
	if n := len(w); n > 3 && w[n-1] == w[n-2] && !strings.ContainsRune("lsz", rune(w[n-1])) {
		w = w[:n-1]
	}
	return w
}

func slugify(s string) string {
	return strings.Trim(nonSlugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}
