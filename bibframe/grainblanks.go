package bibframe

import (
	"path"
	"strings"
)

// Blank-node labels in a merged N-Quads document -- SerializeGrains' catalog.nq
// and the export service's catalog.nq dump, which are required to agree
// (tasks/291).
//
// A grain is stored as canonical N-Quads: its blank nodes already carry `_:c14nN`
// labels that are a pure function of the grain's own graph. Merging used to throw
// that away. Every grain went through one rdf.Encoder, which renames blank nodes
// `_:b1, _:b2, …` in first-encounter order across the whole document, so a label
// depended on how many blank nodes the traversal had already seen. Nothing about
// the catalog had to change for every label to move -- a change in the
// serializer's own traversal order was enough, and one was. The published sha256
// of a 60 MB download changed for a corpus that had not.
//
// Blank-node labels are scoped to the document, so a grain's labels cannot be used
// as they are: two grains both naming a node `_:c14n0` would merge it into one.
// They are namespaced by work id instead, which is unique per grain and stable.
//
// The old scheme had a second, quieter cost. rdf.Encoder.AppendNQuads opens a
// fresh blank-node scope per *graph*, and both writers called it once per graph,
// so a blank node a grain states in two graphs -- one node, by dataset semantics --
// came out as two. On the playground exactly one grain of 2,994 does this, and its
// `_:c14n16` was emitted as two distinct nodes. Under this scheme it cannot
// happen: the label comes from the grain, not from where in the grain the
// traversal first met it.

// grainBlankPrefix namespaces a grain's blank-node labels by its work id. Work
// ids are `w` plus lowercase base32, so they are already legal blank-node label
// characters; anything else is folded to `_` rather than emitted raw, since an
// illegal label would produce a dump no parser accepts.
func GrainBlankPrefix(grainPath string) string {
	id := strings.TrimSuffix(path.Base(grainPath), ".nq")
	var b strings.Builder
	b.Grow(len(id) + 1)
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	b.WriteByte('_')
	return b.String()
}

// relabelBlanks rewrites every blank-node label in one grain's N-Quads bytes to
// prefix+label, leaving the rest of the document byte-for-byte alone.
//
// It scans rather than reparses, so IRIs and literals keep exactly the escaping
// the grain writer produced -- re-serializing would be a second chance to differ.
// A blank node can only appear where a term can, so the scan has to know where
// terms are: `_:c14n0` inside a literal is text, not a node.
func RelabelGrainBlanks(grain []byte, prefix string) []byte {
	out := make([]byte, 0, len(grain)+len(grain)/8)
	for i := 0; i < len(grain); {
		switch c := grain[i]; {
		case c == '<':
			// An IRI. `>` cannot occur inside one unescaped, so this is safe.
			j := i + 1
			for j < len(grain) && grain[j] != '>' && grain[j] != '\n' {
				j++
			}
			if j < len(grain) {
				j++
			}
			out = append(out, grain[i:j]...)
			i = j
		case c == '"':
			// A literal. Skip to the closing quote, honouring backslash escapes;
			// its datatype IRI or language tag is handled by the next iterations.
			j := i + 1
			for j < len(grain) {
				if grain[j] == '\\' && j+1 < len(grain) {
					j += 2
					continue
				}
				if grain[j] == '"' || grain[j] == '\n' {
					break
				}
				j++
			}
			if j < len(grain) && grain[j] == '"' {
				j++
			}
			out = append(out, grain[i:j]...)
			i = j
		case c == '_' && i+1 < len(grain) && grain[i+1] == ':':
			// The label runs to the next whitespace. A statement terminator with
			// no space before it (`_:c14n0.`) would be swallowed into the label,
			// which is harmless: the label is copied through verbatim, so the
			// terminator comes out where it went in. Only the prefix is inserted.
			j := i + 2
			for j < len(grain) && !isNQuadsSpace(grain[j]) {
				j++
			}
			out = append(out, '_', ':')
			out = append(out, prefix...)
			out = append(out, grain[i+2:j]...)
			i = j
		default:
			out = append(out, c)
			i++
		}
	}
	return out
}

func isNQuadsSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
