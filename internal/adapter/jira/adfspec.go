package jira

// ADF vocabulary from @atlaskit/adf-schema 57.6 (dist/json-schema/v1/full.json).
// Nodes and marks not listed here round-trip as <adf-raw> (jira spec §5.4 rule 6).

const adfType = "application/vnd.atlassian.adf+xml"

var adfNodes = map[string]bool{
	"blockCard": true, "blockquote": true, "blockTaskItem": true, "bodiedExtension": true, "bodiedSyncBlock": true,
	"bulletList": true, "caption": true, "codeBlock": true, "date": true, "decisionItem": true, "decisionList": true,
	"embedCard": true, "emoji": true, "expand": true, "extension": true, "hardBreak": true, "heading": true,
	"inlineCard": true, "inlineExtension": true, "layoutColumn": true, "layoutSection": true, "listItem": true,
	"media": true, "mediaGroup": true, "mediaInline": true, "mediaSingle": true, "mention": true, "nestedExpand": true,
	"orderedList": true, "panel": true, "paragraph": true, "placeholder": true, "rule": true, "status": true,
	"syncBlock": true, "table": true, "tableCell": true, "tableHeader": true, "tableRow": true, "taskItem": true,
	"taskList": true,
}

// adfMarks lists mark types in wrapper order, outermost first. No mark type
// is also a node type, so a wrapper element is never ambiguous.
var adfMarks = []string{"link", "annotation", "dataConsumer", "fragment", "border", "alignment", "indentation", "breakout",
	"strong", "em", "underline", "strike", "code", "subsup", "textColor", "backgroundColor", "fontSize"}

type attrKind int

const (
	kString attrKind = iota // the default: every attribute not listed below
	kNumber
	kBool
	kJSON // object, array or any value: kept as compact JSON text
)

// attrKinds lists the attributes whose JSON type is not a string.
var attrKinds = map[string]map[string]attrKind{
	"blockCard":       {"data": kJSON, "datasource": kJSON, "parameters": kJSON, "properties": kJSON, "views": kJSON, "width": kNumber},
	"bodiedExtension": {"parameters": kJSON},
	"border":          {"size": kNumber},
	"breakout":        {"width": kNumber},
	"codeBlock":       {"hideLineNumbers": kBool, "wrap": kBool},
	"dataConsumer":    {"sources": kJSON},
	"embedCard":       {"originalHeight": kNumber, "originalWidth": kNumber, "width": kNumber},
	"extension":       {"parameters": kJSON},
	"heading":         {"level": kNumber},
	"indentation":     {"level": kNumber},
	"inlineCard":      {"data": kJSON},
	"inlineExtension": {"parameters": kJSON},
	"layoutColumn":    {"width": kNumber},
	"media":           {"height": kNumber, "width": kNumber},
	"mediaInline":     {"data": kJSON, "height": kNumber, "width": kNumber},
	"mediaSingle":     {"width": kNumber},
	"orderedList":     {"order": kNumber},
	"table":           {"isNumberColumnEnabled": kBool, "width": kNumber},
	"tableCell":       {"colspan": kNumber, "colwidth": kJSON, "rowspan": kNumber},
	"tableHeader":     {"colspan": kNumber, "colwidth": kJSON, "rowspan": kNumber},
}
