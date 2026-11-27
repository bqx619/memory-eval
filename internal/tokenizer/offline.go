// Package tokenizer installs the already-approved embedded BPE assets once.
package tokenizer

import (
	"github.com/pkoukk/tiktoken-go"
	loader "github.com/pkoukk/tiktoken-go-loader"
)

func init()                                            { tiktoken.SetBpeLoader(loader.NewOfflineLoader()) }
func Encoding(name string) (*tiktoken.Tiktoken, error) { return tiktoken.GetEncoding(name) }
