package larian

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

// LSX is Larian's XML resource format, the text form of its LSF files:
//
//	<save>
//	  <version major=".." minor=".." revision=".." build=".."/>
//	  <region id="...">
//	    <node id="...">
//	      <attribute id="..." type="..." value="..."/>
//	      <children>
//	        <node id="...">...</node>
//	      </children>
//	    </node>
//	  </region>
//	</save>
//
// [NMA] quotes a meta.lsx and a modsettings.lsx in this shape. parseLSX
// reads it into a tree of nodes, each with its attributes and the byte
// range of its element in the input, so a caller can splice one node out
// and keep every other byte. Elements it does not know are skipped.

// lsxNode is one <node> element.
type lsxNode struct {
	ID       string
	Attrs    []lsxAttr
	Children []*lsxNode
	// Start and End are the byte range of the whole <node> element in the
	// input, end tag included.
	Start, End int
}

// lsxAttr is one <attribute> of a node.
type lsxAttr struct {
	ID, Type, Value string
}

// attr is the value of n's attribute id, and whether it has one.
func (n *lsxNode) attr(id string) (string, bool) {
	for _, a := range n.Attrs {
		if a.ID == id {
			return a.Value, true
		}
	}
	return "", false
}

// child is n's first child node with the given id.
func (n *lsxNode) child(id string) *lsxNode {
	for _, c := range n.Children {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// lsxRegion is one <region> and its top-level nodes.
type lsxRegion struct {
	ID    string
	Nodes []*lsxNode
}

// lsxDoc is a parsed document.
type lsxDoc struct {
	Regions []*lsxRegion
}

// region is the document's first region with the given id.
func (d *lsxDoc) region(id string) *lsxRegion {
	for _, r := range d.Regions {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// node is r's first top-level node with the given id.
func (r *lsxRegion) node(id string) *lsxNode {
	for _, n := range r.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// LSX limits: no real file comes near them.
const (
	maxLSXNodes = 200_000
	maxLSXDepth = 64
)

// errLSX is every LSX parse refusal.
var errLSX = errors.New("not a readable LSX document")

// utf8BOM is the byte-order mark a file may start with.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// parseLSX parses data. Offsets are into data itself, a leading BOM
// included. Only UTF-8 is read: a document declaring another encoding is
// refused.
func parseLSX(data []byte) (*lsxDoc, error) {
	base := 0
	if bytes.HasPrefix(data, utf8BOM) {
		base = len(utf8BOM)
	}
	d := xml.NewDecoder(bytes.NewReader(data[base:]))
	d.Strict = true
	doc := &lsxDoc{}

	type frame struct {
		kind   string // save, region, node, children, attribute, skip
		node   *lsxNode
		region *lsxRegion
	}
	var stack []frame
	nodes := 0
	sawSave := false
	for {
		off := int(d.InputOffset())
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errLSX, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) >= maxLSXDepth {
				return nil, fmt.Errorf("%w: nested deeper than %d", errLSX, maxLSXDepth)
			}
			parent := ""
			if len(stack) > 0 {
				parent = stack[len(stack)-1].kind
			}
			f := frame{kind: "skip"}
			name := t.Name.Local
			switch {
			case len(stack) == 0:
				if name != "save" || sawSave {
					return nil, fmt.Errorf("%w: the root element is <%s>, not <save>", errLSX, name)
				}
				sawSave = true
				f.kind = "save"
			case name == "region" && parent == "save":
				f.kind = "region"
				f.region = &lsxRegion{ID: xmlAttr(t, "id")}
				doc.Regions = append(doc.Regions, f.region)
			case name == "node" && (parent == "region" || parent == "children"):
				nodes++
				if nodes > maxLSXNodes {
					return nil, fmt.Errorf("%w: more than %d nodes", errLSX, maxLSXNodes)
				}
				f.kind = "node"
				f.node = &lsxNode{ID: xmlAttr(t, "id"), Start: base + off}
				owner := stack[len(stack)-1]
				if parent == "region" {
					owner.region.Nodes = append(owner.region.Nodes, f.node)
				} else {
					owner.node.Children = append(owner.node.Children, f.node)
				}
			case name == "children" && parent == "node":
				f.kind = "children"
				f.node = stack[len(stack)-1].node
			case name == "attribute" && parent == "node":
				f.kind = "attribute"
				n := stack[len(stack)-1].node
				n.Attrs = append(n.Attrs, lsxAttr{ID: xmlAttr(t, "id"), Type: xmlAttr(t, "type"), Value: xmlAttr(t, "value")})
			}
			stack = append(stack, f)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("%w: an end tag with no element open", errLSX)
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if top.kind == "node" {
				top.node.End = base + int(d.InputOffset())
			}
		}
	}
	if !sawSave || len(stack) != 0 {
		return nil, fmt.Errorf("%w: no complete <save> element", errLSX)
	}
	return doc, nil
}

// xmlAttr is the value of a start element's attribute.
func xmlAttr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}
