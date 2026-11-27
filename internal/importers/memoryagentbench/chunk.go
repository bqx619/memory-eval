package memoryagentbench

import (
	"fmt"
	"github.com/neurosnap/sentences"
	"github.com/neurosnap/sentences/english"
	"github.com/pkoukk/tiktoken-go"
	"memeval/internal/tokenizer"
	"strings"
	"sync"
)

var chunkEncoding = sync.OnceValues(func() (*tiktoken.Tiktoken, error) {
	return tokenizer.Encoding("o200k_base")
})

type Chunker struct {
	sentence *sentences.DefaultSentenceTokenizer
	encoding *tiktoken.Tiktoken
}
type Chunk struct {
	Text           string `json:"text"`
	SentenceTokens int    `json:"sentence_tokens"`
	Tokens         int    `json:"tokens"`
}

func NewChunker() (*Chunker, error) {
	e, err := chunkEncoding()
	if err != nil {
		return nil, err
	}
	s, err := english.NewSentenceTokenizer(nil)
	return &Chunker{s, e}, err
}
func (c *Chunker) Tokens(s string) int {
	return len(c.encoding.Encode(s, []string{"<|endoftext|>"}, nil))
}
func (c *Chunker) Chunk(text string, limit int) ([]Chunk, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("chunk size must be positive")
	}
	out := []Chunk{}
	current := []string{}
	n := 0
	flush := func() { s := strings.Join(current, " "); out = append(out, Chunk{s, n, c.Tokens(s)}) }
	for _, sentence := range c.sentence.Tokenize(text) {
		s := strings.TrimSpace(sentence.Text)
		k := c.Tokens(s)
		if n+k > limit {
			flush()
			current = []string{s}
			n = k
		} else {
			current = append(current, s)
			n += k
		}
	}
	if len(current) > 0 {
		flush()
	}
	return out, nil
}
