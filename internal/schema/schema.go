// Package schema declares the shape of an adapter's resource root.
package schema

import "slices"

type Kind int

const (
	Field      Kind = iota // scalar text element (Repeated: may occur several times)
	List                   // container of repeated Item elements
	Body                   // native service content, passed through
	Sub                    // sub-resource with identity attribute ID
	Attachment             // metadata of binary content whose bytes live in the sidecar folder
)

type Attr struct {
	Name     string
	ReadOnly bool // written by gfs from the service; a local change is a commit error
}

type Elem struct {
	Name      string
	Kind      Kind
	Repeated  bool     // Field only
	Item      string   // List only: item element name
	Sorted    bool     // List only: items are unordered, sort by text
	Attrs     []Attr   // declared attributes in canonical order
	ID        string   // Sub, Attachment: identity attribute
	SortKey   string   // Sub, Attachment: attribute to sort by
	Children  []Elem   // Sub only: declared nested elements (e.g. reply)
	BodyTypes []string // Body only: allowed values of the type attribute

	NameAttr    string   // Attachment: attribute holding the service's file name
	VersionAttr string   // Attachment: version attribute, "" if the service has none
	Ops         []string // Attachment: allowed operations (create, update, delete); none = read-only
	PrefixAttr  string   // Attachment: attribute of the parent element prepended to file names
	MaxSize     int64    // Attachment: largest upload in bytes, 0 = no limit
}

type Schema struct {
	Root      string
	RootAttrs []Attr
	ID        string // identity attribute of the root
	Version   string // version attribute of the root, "" if none
	Elems     []Elem // root children in canonical order
}

func Find(elems []Elem, name string) *Elem {
	for i := range elems {
		if elems[i].Name == name {
			return &elems[i]
		}
	}
	return nil
}

func AttrDecl(attrs []Attr, name string) (Attr, bool) {
	for _, a := range attrs {
		if a.Name == name {
			return a, true
		}
	}
	return Attr{}, false
}

// Attachment returns the root-level attachment element, or nil if the
// adapter has no attachments.
func (s *Schema) Attachment() *Elem {
	for i := range s.Elems {
		if s.Elems[i].Kind == Attachment {
			return &s.Elems[i]
		}
	}
	return nil
}

// Allows reports whether an attachment kind permits op (create, update, delete).
func (e *Elem) Allows(op string) bool { return slices.Contains(e.Ops, op) }
