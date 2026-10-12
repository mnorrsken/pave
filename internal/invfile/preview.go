package invfile

import (
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// previewLimit is how much of a file a preview reads. A vars file is a few
// lines; anything much bigger is one key away in the editor.
const previewLimit = 256 << 10

// Preview is what the browser shows of a file without opening it: the text of
// a plain file, and only the keys of a sops one, whose values are ciphertext.
// An ansible-vault file is one block of ciphertext and has nothing to show, so
// it gets an empty preview.
func Preview(f File) (string, error) {
	if !f.Exists || f.Kind == KindVault {
		return "", nil
	}
	b, cut, err := readHead(f.Path)
	if err != nil {
		return "", err
	}
	if f.Kind == KindSops {
		if cut {
			return "", fmt.Errorf("%s is too big to preview", f.Path)
		}
		return sopsKeys(b)
	}
	s := string(b)
	if cut {
		// Stop at the last whole line rather than in the middle of one.
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			s = s[:i+1]
		}
		s += "…\n"
	}
	return s, nil
}

// readHead reads at most previewLimit bytes, and says whether there was more.
func readHead(path string) ([]byte, bool, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, previewLimit+1))
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	if len(b) > previewLimit {
		return b[:previewLimit], true, nil
	}
	return b, false, nil
}

// sopsKeys is the outline of a sops file: every key, nested the way the file
// nests them, and none of the values. The top-level sops key is sops' own
// metadata, not a variable, and is left out. yaml reads the json flavour too.
func sopsKeys(b []byte) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	var out strings.Builder
	if len(doc.Content) > 0 {
		writeKeys(&out, doc.Content[0], "", true)
	}
	return out.String(), nil
}

// writeKeys writes the keys under n. A key with something nested under it
// ends in a colon, a key with a value does not; a list item is a dash.
func writeKeys(out *strings.Builder, n *yaml.Node, indent string, top bool) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if top && key.Value == "sops" {
				continue
			}
			if val.Kind == yaml.MappingNode || val.Kind == yaml.SequenceNode {
				fmt.Fprintf(out, "%s%s:\n", indent, key.Value)
				writeKeys(out, val, indent+"  ", false)
				continue
			}
			fmt.Fprintf(out, "%s%s\n", indent, key.Value)
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if item.Kind != yaml.MappingNode && item.Kind != yaml.SequenceNode {
				fmt.Fprintf(out, "%s-\n", indent)
				continue
			}
			// The first key goes on the dash's own line, as yaml writes it.
			var sub strings.Builder
			writeKeys(&sub, item, indent+"  ", false)
			s := sub.String()
			if s == "" {
				fmt.Fprintf(out, "%s-\n", indent)
				continue
			}
			out.WriteString(indent + "- " + strings.TrimPrefix(s, indent+"  "))
		}
	}
}
