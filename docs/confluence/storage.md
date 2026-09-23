# Confluence storage reference

Generated from `docs/confluence/examples` by `TestStorageCatalogue -update`; do not edit by hand.

Each variant is a body fragment in Confluence storage format, as it sits inside `<body>`.
Status comes from the last live round trip (`TestStorageExamples`):

* **same**: Confluence stores the fragment unchanged, ignoring `ac:macro-id`, local ids, `ri:version-at-save` and the order of macro parameters. Safe to write.
* **changed**: Confluence rewrites it; the stored form is shown. Write the stored form to avoid a diff on the next pull.
* **untested**: not part of the last round trip.

Print one fragment with `gfs example confluence <node>/<variant>`; validate a page with
`gfs schema confluence > storage.rng && xmllint --noout --relaxng storage.rng <page.xml>`.

## Contents

* [blockquote](#blockquote): list, paragraphs
* [bulletList](#bulletlist): nested, rich-item
* [card](#card): block, embed-center, embed-wide, inline
* [codeBlock](#codeblock): language, options, plain
* [date](#date): inline
* [decisionList](#decisionlist): decided
* [emoji](#emoji): atlassian, unicode
* [expand](#expand): rich, titled, untitled
* [heading](#heading): aligned, levels
* [layout](#layout): types, wide-rich
* [link](#link): anchor, attachment, external, page, page-text
* [macro-attachments](#macro-attachments): list
* [macro-children](#macro-children): all
* [macro-contentbylabel](#macro-contentbylabel): cql
* [macro-details](#macro-details): properties, report
* [macro-excerpt](#macro-excerpt): excerpt, include
* [macro-gallery](#macro-gallery): columns
* [macro-include](#macro-include): page
* [macro-jira](#macro-jira): jql
* [macro-livesearch](#macro-livesearch): space
* [macro-noformat](#macro-noformat): text
* [macro-pagetree](#macro-pagetree): home
* [macro-profile](#macro-profile): user
* [macro-recently-updated](#macro-recently-updated): max
* [macro-section](#macro-section): columns
* [macro-tasks-report](#macro-tasks-report): space
* [macro-toc](#macro-toc): levels
* [marks](#marks): background-color, basic, combined, text-color
* [mediaGroup](#mediagroup): file
* [mediaInline](#mediainline): image
* [mediaSingle](#mediasingle): align, center, external, linked, wide, width-border, wrap
* [mention](#mention): user
* [orderedList](#orderedlist): nested, start
* [panel](#panel): custom-icon, custom-plain, error, info, note, rich, success, warning
* [paragraph](#paragraph): aligned, hard-break, indented, placeholder, plain
* [rule](#rule): hr
* [status](#status): colours, subtle
* [table](#table): cell-colours, full-width-rich, header-column, header-row, spans-widths
* [taskList](#tasklist): due-date, nested, states
* [Rejected forms](#rejected-forms)

## blockquote

### list (same)

blockquote containing a list

```xml
<blockquote>
  <ul>
    <li>
      <p>quoted list</p>
    </li>
  </ul>
</blockquote>
```

### paragraphs (same)

blockquote with two paragraphs

```xml
<blockquote>
  <p>Quoted.</p>
  <p>Second paragraph.</p>
</blockquote>
```

## bulletList

### nested (same)

bullet list, three levels

```xml
<ul>
  <li>
    <p>item</p>
  </li>
  <li>
    <p>parent</p>
    <ul>
      <li>
        <p>nested</p>
        <ul>
          <li>
            <p>third level</p>
          </li>
        </ul>
      </li>
    </ul>
  </li>
</ul>
```

### rich-item (same)

list item with marks and a link

```xml
<ul>
  <li>
    <p>item with <strong>marks</strong> and a <a href="https://example.com">link</a></p>
  </li>
</ul>
```

## card

### block (same)

blockCard

```xml
<p>
  <a href="https://www.atlassian.com/software/confluence" data-card-appearance="block">https://www.atlassian.com/software/confluence</a>
</p>
```

### embed-center (same)

embedCard centered

```xml
<p>
  <a href="https://www.youtube.com/watch?v=dQw4w9WgXcQ" data-card-appearance="embed" data-layout="center" data-width="100">https://www.youtube.com/watch?v=dQw4w9WgXcQ</a>
</p>
```

### embed-wide (same)

embedCard wide

```xml
<p>
  <a href="https://www.youtube.com/watch?v=dQw4w9WgXcQ" data-card-appearance="embed" data-layout="wide">https://www.youtube.com/watch?v=dQw4w9WgXcQ</a>
</p>
```

### inline (same)

inlineCard smart link

```xml
<p>
  <a href="https://www.atlassian.com/software/confluence" data-card-appearance="inline">https://www.atlassian.com/software/confluence</a>
</p>
```

## codeBlock

### language (same)

code block with language, escaping-sensitive content

```xml
<ac:structured-macro ac:name="code" ac:schema-version="1">
  <ac:parameter ac:name="language">go</ac:parameter>
  <ac:plain-text-body><![CDATA[package main

import "fmt"

func main() { fmt.Println("<hello & goodbye>") }]]></ac:plain-text-body>
</ac:structured-macro>
```

### options (same)

title, line numbers, theme, collapse

```xml
<ac:structured-macro ac:name="code" ac:schema-version="1">
  <ac:parameter ac:name="language">bash</ac:parameter>
  <ac:parameter ac:name="title">Example</ac:parameter>
  <ac:parameter ac:name="linenumbers">true</ac:parameter>
  <ac:parameter ac:name="theme">Midnight</ac:parameter>
  <ac:parameter ac:name="collapse">true</ac:parameter>
  <ac:plain-text-body>gfs status</ac:plain-text-body>
</ac:structured-macro>
```

### plain (same)

code block without language

```xml
<ac:structured-macro ac:name="code" ac:schema-version="1">
  <ac:plain-text-body>plain code block</ac:plain-text-body>
</ac:structured-macro>
```

## date

### inline (same)

date nodes

```xml
<p><time datetime="2026-09-24"/> and <time datetime="2027-01-01"/></p>
```

## decisionList

### decided (same)

decision list with two decisions

```xml
<ac:adf-extension>
  <ac:adf-node type="decision-list">
    <ac:adf-attribute key="local-id">decision-list-1</ac:adf-attribute>
    <ac:adf-node type="decision-item">
      <ac:adf-attribute key="local-id">decision-1</ac:adf-attribute>
      <ac:adf-attribute key="state">DECIDED</ac:adf-attribute>
      <ac:adf-content>First decision.</ac:adf-content>
    </ac:adf-node>
    <ac:adf-node type="decision-item">
      <ac:adf-attribute key="local-id">decision-2</ac:adf-attribute>
      <ac:adf-attribute key="state">DECIDED</ac:adf-attribute>
      <ac:adf-content>Second decision with <strong>marks</strong>.</ac:adf-content>
    </ac:adf-node>
  </ac:adf-node>
</ac:adf-extension>
```

## emoji

### atlassian (same)

atlassian emoji

```xml
<p>
  <ac:emoticon ac:name="tick" ac:emoji-shortname=":check_mark:" ac:emoji-id="atlassian-check_mark" ac:emoji-fallback=":check_mark:"/>
  <ac:emoticon ac:name="cross" ac:emoji-shortname=":cross_mark:" ac:emoji-id="atlassian-cross_mark" ac:emoji-fallback=":cross_mark:"/>
  <ac:emoticon ac:name="warning" ac:emoji-shortname=":warning:" ac:emoji-id="atlassian-warning" ac:emoji-fallback=":warning:"/>
  <ac:emoticon ac:name="information" ac:emoji-shortname=":info:" ac:emoji-id="atlassian-info" ac:emoji-fallback=":info:"/>
</p>
```

### unicode (same)

unicode emoji

```xml
<p>
  <ac:emoticon ac:name="smile" ac:emoji-shortname=":smile:" ac:emoji-id="1f604" ac:emoji-fallback="😄"/>
  <ac:emoticon ac:name="blue-star" ac:emoji-shortname=":thumbsup:" ac:emoji-id="1f44d" ac:emoji-fallback="👍"/>
</p>
```

## expand

### rich (same)

expand with panel and table inside

```xml
<ac:structured-macro ac:name="expand" ac:schema-version="1">
  <ac:parameter ac:name="title">Rich</ac:parameter>
  <ac:rich-text-body>
    <ac:structured-macro ac:name="note" ac:schema-version="1">
      <ac:rich-text-body>
        <p>panel</p>
      </ac:rich-text-body>
    </ac:structured-macro>
    <table>
      <tbody>
        <tr>
          <th>
            <p>a</p>
          </th>
        </tr>
        <tr>
          <td>
            <p>1</p>
          </td>
        </tr>
      </tbody>
    </table>
  </ac:rich-text-body>
</ac:structured-macro>
```

### titled (same)

expand with title

```xml
<ac:structured-macro ac:name="expand" ac:schema-version="1">
  <ac:parameter ac:name="title">Expand with title</ac:parameter>
  <ac:rich-text-body>
    <p>Hidden.</p>
  </ac:rich-text-body>
</ac:structured-macro>
```

### untitled (same)

expand without title

```xml
<ac:structured-macro ac:name="expand" ac:schema-version="1">
  <ac:rich-text-body>
    <p>No title.</p>
  </ac:rich-text-body>
</ac:structured-macro>
```

## heading

### aligned (same)

headings with alignment

```xml
<h2 style="text-align: center;">Centered heading</h2>
<h3 style="text-align: right;">Right-aligned heading</h3>
```

### levels (same)

h1 to h6

```xml
<h1>Heading level 1</h1>
<h2>Heading level 2</h2>
<h3>Heading level 3</h3>
<h4>Heading level 4</h4>
<h5>Heading level 5</h5>
<h6>Heading level 6</h6>
```

## layout

### types (same)

every layout section type

```xml
<ac:layout>
  <ac:layout-section ac:type="single">
    <ac:layout-cell>
      <p>single 1</p>
    </ac:layout-cell>
  </ac:layout-section>
  <ac:layout-section ac:type="two_equal">
    <ac:layout-cell>
      <p>two_equal 1</p>
    </ac:layout-cell>
    <ac:layout-cell>
      <p>two_equal 2</p>
    </ac:layout-cell>
  </ac:layout-section>
  <ac:layout-section ac:type="two_left_sidebar">
    <ac:layout-cell>
      <p>two_left_sidebar 1</p>
    </ac:layout-cell>
    <ac:layout-cell>
      <p>two_left_sidebar 2</p>
    </ac:layout-cell>
  </ac:layout-section>
  <ac:layout-section ac:type="two_right_sidebar">
    <ac:layout-cell>
      <p>two_right_sidebar 1</p>
    </ac:layout-cell>
    <ac:layout-cell>
      <p>two_right_sidebar 2</p>
    </ac:layout-cell>
  </ac:layout-section>
  <ac:layout-section ac:type="three_equal">
    <ac:layout-cell>
      <p>three_equal 1</p>
    </ac:layout-cell>
    <ac:layout-cell>
      <p>three_equal 2</p>
    </ac:layout-cell>
    <ac:layout-cell>
      <p>three_equal 3</p>
    </ac:layout-cell>
  </ac:layout-section>
  <ac:layout-section ac:type="three_with_sidebars">
    <ac:layout-cell>
      <p>three_with_sidebars 1</p>
    </ac:layout-cell>
    <ac:layout-cell>
      <p>three_with_sidebars 2</p>
    </ac:layout-cell>
    <ac:layout-cell>
      <p>three_with_sidebars 3</p>
    </ac:layout-cell>
  </ac:layout-section>
</ac:layout>
```

### wide-rich (same)

wide layout with panel and table

```xml
<ac:layout>
  <ac:layout-section ac:type="two_equal" ac:breakout-mode="wide">
    <ac:layout-cell>
      <ac:structured-macro ac:name="info" ac:schema-version="1">
        <ac:rich-text-body>
          <p>panel in layout</p>
        </ac:rich-text-body>
      </ac:structured-macro>
    </ac:layout-cell>
    <ac:layout-cell>
      <table>
        <tbody>
          <tr>
            <td>
              <p>table in layout</p>
            </td>
          </tr>
        </tbody>
      </table>
    </ac:layout-cell>
  </ac:layout-section>
</ac:layout>
```

## link

### anchor (same)

anchor macro and a link to it

```xml
<p><ac:structured-macro ac:name="anchor" ac:schema-version="1"><ac:parameter ac:name="">anchor-target</ac:parameter></ac:structured-macro>Anchor target.</p>
<p>
  <ac:link ac:anchor="anchor-target">
    <ac:plain-text-link-body>jump to the anchor</ac:plain-text-link-body>
  </ac:link>
</p>
```

### attachment (same)

link to an attachment of this page

```xml
<p>
  <ac:link>
    <ri:attachment ri:filename="notes.txt"/>
    <ac:plain-text-link-body>notes.txt</ac:plain-text-link-body>
  </ac:link>
</p>
```

### external (same)

external link and mailto

```xml
<p>
  <a href="https://www.atlassian.com">external</a>
  <a href="mailto:someone@example.com">someone@example.com</a>
</p>
```

### page (same)

link to a page

```xml
<p>
  <ac:link>
    <ri:page ri:content-title="Handoff Home"/>
  </ac:link>
</p>
```

### page-text (same)

link to a page with custom text

```xml
<p>
  <ac:link>
    <ri:page ri:content-title="Handoff Home"/>
    <ac:plain-text-link-body>go home</ac:plain-text-link-body>
  </ac:link>
</p>
```

## macro-attachments

### list (same)

attachments list

```xml
<ac:structured-macro ac:name="attachments" ac:schema-version="1">
  <ac:parameter ac:name="upload">false</ac:parameter>
</ac:structured-macro>
```

## macro-children

### all (same)

children display

```xml
<ac:structured-macro ac:name="children" ac:schema-version="2">
  <ac:parameter ac:name="all">true</ac:parameter>
  <ac:parameter ac:name="sort">title</ac:parameter>
</ac:structured-macro>
```

## macro-contentbylabel

### cql (same)

content by label

```xml
<ac:structured-macro ac:name="contentbylabel" ac:schema-version="4">
  <ac:parameter ac:name="cql">label = "gfs-test"</ac:parameter>
</ac:structured-macro>
```

## macro-details

### properties (same)

page properties

```xml
<ac:structured-macro ac:name="details" ac:schema-version="1">
  <ac:parameter ac:name="id">gfs-props</ac:parameter>
  <ac:rich-text-body>
    <table>
      <tbody>
        <tr>
          <th>
            <p>Owner</p>
          </th>
          <td>
            <p>gfs</p>
          </td>
        </tr>
      </tbody>
    </table>
  </ac:rich-text-body>
</ac:structured-macro>
```

### report (same)

page properties report

```xml
<ac:structured-macro ac:name="detailssummary" ac:schema-version="3">
  <ac:parameter ac:name="cql">label = "gfs-test"</ac:parameter>
</ac:structured-macro>
```

## macro-excerpt

### excerpt (same)

named excerpt

```xml
<ac:structured-macro ac:name="excerpt" ac:schema-version="1">
  <ac:parameter ac:name="name">gfs-excerpt</ac:parameter>
  <ac:rich-text-body>
    <p>Excerpt text.</p>
  </ac:rich-text-body>
</ac:structured-macro>
```

### include (same)

excerpt include from a page

```xml
<ac:structured-macro ac:name="excerpt-include" ac:schema-version="1">
  <ac:parameter ac:name="">
    <ac:link>
      <ri:page ri:content-title="Handoff Home"/>
    </ac:link>
  </ac:parameter>
  <ac:parameter ac:name="nopanel">true</ac:parameter>
</ac:structured-macro>
```

## macro-gallery

### columns (same)

image gallery

```xml
<ac:structured-macro ac:name="gallery" ac:schema-version="1">
  <ac:parameter ac:name="columns">3</ac:parameter>
</ac:structured-macro>
```

## macro-include

### page (same)

include page

```xml
<ac:structured-macro ac:name="include" ac:schema-version="1">
  <ac:parameter ac:name="">
    <ac:link>
      <ri:page ri:content-title="Handoff Home"/>
    </ac:link>
  </ac:parameter>
</ac:structured-macro>
```

## macro-jira

### jql (same)

Jira issues by JQL (needs a linked Jira site)

```xml
<ac:structured-macro ac:name="jira" ac:schema-version="1">
  <ac:parameter ac:name="jqlQuery">project = HF ORDER BY created DESC</ac:parameter>
  <ac:parameter ac:name="maximumIssues">5</ac:parameter>
</ac:structured-macro>
```

## macro-livesearch

### space (same)

live search

```xml
<ac:structured-macro ac:name="livesearch" ac:schema-version="1">
  <ac:parameter ac:name="spaceKey">
    <ri:space ri:space-key="HF"/>
  </ac:parameter>
</ac:structured-macro>
```

## macro-noformat

### text (same)

noformat block

```xml
<ac:structured-macro ac:name="noformat" ac:schema-version="1">
  <ac:plain-text-body><![CDATA[keeps   spacing
and <markup> as text]]></ac:plain-text-body>
</ac:structured-macro>
```

## macro-pagetree

### home (same)

page tree from the space home

```xml
<ac:structured-macro ac:name="pagetree" ac:schema-version="1">
  <ac:parameter ac:name="root">
    <ac:link>
      <ri:page ri:content-title="@home"/>
    </ac:link>
  </ac:parameter>
  <ac:parameter ac:name="expandCollapseAll">true</ac:parameter>
</ac:structured-macro>
```

## macro-profile

### user (same)

user profile

```xml
<ac:structured-macro ac:name="profile" ac:schema-version="1">
  <ac:parameter ac:name="user">
    <ri:user ri:account-id="557058:e1cec712-baf5-4d71-a033-12684497b221"/>
  </ac:parameter>
</ac:structured-macro>
```

## macro-recently-updated

### max (same)

recently updated

```xml
<ac:structured-macro ac:name="recently-updated" ac:schema-version="1">
  <ac:parameter ac:name="max">5</ac:parameter>
</ac:structured-macro>
```

## macro-section

### columns (same)

legacy section and columns

```xml
<ac:structured-macro ac:name="section" ac:schema-version="1">
  <ac:rich-text-body>
    <ac:structured-macro ac:name="column" ac:schema-version="1">
      <ac:parameter ac:name="width">30%</ac:parameter>
      <ac:rich-text-body>
        <p>30%</p>
      </ac:rich-text-body>
    </ac:structured-macro>
    <ac:structured-macro ac:name="column" ac:schema-version="1">
      <ac:rich-text-body>
        <p>rest</p>
      </ac:rich-text-body>
    </ac:structured-macro>
  </ac:rich-text-body>
</ac:structured-macro>
```

## macro-tasks-report

### space (same)

task report

```xml
<ac:structured-macro ac:name="tasks-report-macro" ac:schema-version="1">
  <ac:parameter ac:name="spaces">HF</ac:parameter>
  <ac:parameter ac:name="status">incomplete</ac:parameter>
</ac:structured-macro>
```

## macro-toc

### levels (same)

table of contents, levels 1-2

```xml
<ac:structured-macro ac:name="toc" ac:schema-version="1">
  <ac:parameter ac:name="minLevel">1</ac:parameter>
  <ac:parameter ac:name="maxLevel">2</ac:parameter>
</ac:structured-macro>
```

## marks

### background-color (same)

backgroundColor (text highlight): a background-color style in rgb(); hex values are converted to rgb

```xml
<p>
  <span style="background-color: rgb(220,223,228);">grey</span>
  <span style="background-color: rgb(198,237,251);">teal</span>
  <span style="background-color: rgb(211,241,167);">lime</span>
  <span style="background-color: rgb(254,222,200);">orange</span>
  <span style="background-color: rgb(253,208,236);">magenta</span>
  <span style="background-color: rgb(223,216,253);">purple</span>
  <span style="background-color: rgb(248,230,160);">yellow</span>
</p>
```

### basic (same)

strong, em, underline, strike, code, sub, sup

```xml
<p><strong>strong</strong> <em>em</em> <u>underline</u> <s>strike</s> <code>code</code> x<sub>sub</sub> x<sup>sup</sup></p>
```

### combined (same)

combined marks and marks inside links

```xml
<p>
  <strong>
    <em>strong em</em>
  </strong>
  <strong>
    <u>
      <s>strong underline strike</s>
    </u>
  </strong>
  <a href="https://www.atlassian.com">
    <strong>bold link</strong>
  </a>
  <a href="https://www.atlassian.com">
    <code>code link</code>
  </a>
</p>
```

### text-color (same)

textColor palette

```xml
<p>
  <span style="color: rgb(23,43,77);">dark blue</span>
  <span style="color: rgb(7,71,166);">blue</span>
  <span style="color: rgb(0,141,166);">teal</span>
  <span style="color: rgb(0,102,68);">green</span>
  <span style="color: rgb(255,153,31);">orange</span>
  <span style="color: rgb(191,38,0);">red</span>
  <span style="color: rgb(64,50,148);">purple</span>
  <span style="color: rgb(151,160,175);">grey</span>
</p>
```

## mediaGroup

### file (same)

file card for an attachment

```xml
<ac:structured-macro ac:name="view-file" ac:schema-version="1">
  <ac:parameter ac:name="name">
    <ri:attachment ri:filename="notes.txt"/>
  </ac:parameter>
</ac:structured-macro>
```

## mediaInline

### image (same)

inline image in text

```xml
<p>before <ac:image ac:inline="true" ac:height="16"><ri:attachment ri:filename="gradient.png"/></ac:image> after</p>
```

## mediaSingle

### align (same)

aligned start and end

```xml
<ac:image ac:align="left" ac:layout="align-start" ac:width="120">
  <ri:attachment ri:filename="gradient.png"/>
</ac:image>
<ac:image ac:align="right" ac:layout="align-end" ac:width="120">
  <ri:attachment ri:filename="gradient.png"/>
</ac:image>
```

### center (same)

attached image, centered

```xml
<ac:image ac:align="center" ac:layout="center" ac:alt="gradient">
  <ri:attachment ri:filename="gradient.png"/>
</ac:image>
```

### external (same)

external image by URL

```xml
<ac:image ac:align="center" ac:layout="center" ac:width="240">
  <ri:url ri:value="https://wac-cdn.atlassian.com/dam/jcr:e33efd9e-e0b8-4d61-a24d-68a48ef99ed5/Confluence-blue.svg"/>
</ac:image>
```

### linked (same)

image with a link

```xml
<p>
  <a href="https://www.atlassian.com">
    <ac:image ac:width="120">
      <ri:attachment ri:filename="gradient.png"/>
    </ac:image>
  </a>
</p>
```

### wide (same)

wide and full-width

```xml
<ac:image ac:layout="wide">
  <ri:attachment ri:filename="gradient.png"/>
</ac:image>
<ac:image ac:layout="full-width">
  <ri:attachment ri:filename="gradient.png"/>
</ac:image>
```

### width-border (same)

fixed width and border

```xml
<ac:image ac:align="center" ac:layout="center" ac:width="200" ac:border="true">
  <ri:attachment ri:filename="gradient.png"/>
</ac:image>
```

### wrap (same)

wrap left and right

```xml
<ac:image ac:layout="wrap-left" ac:width="120">
  <ri:attachment ri:filename="gradient.png"/>
</ac:image>
<p>Text wrapping around the image.</p>
<ac:image ac:layout="wrap-right" ac:width="120">
  <ri:attachment ri:filename="gradient.png"/>
</ac:image>
<p>Text wrapping around the image.</p>
```

## mention

### user (same)

mention by account id

```xml
<p>
  <ac:link>
    <ri:user ri:account-id="557058:e1cec712-baf5-4d71-a033-12684497b221"/>
  </ac:link>
</p>
```

## orderedList

### nested (same)

ordered list with nested ordered and bullet lists

```xml
<ol>
  <li>
    <p>one</p>
  </li>
  <li>
    <p>two</p>
    <ol>
      <li>
        <p>nested one</p>
      </li>
    </ol>
  </li>
  <li>
    <p>three</p>
    <ul>
      <li>
        <p>mixed</p>
      </li>
    </ul>
  </li>
</ol>
```

### start (same)

ordered list starting at 5

```xml
<ol start="5">
  <li>
    <p>five</p>
  </li>
  <li>
    <p>six</p>
  </li>
</ol>
```

## panel

### custom-icon (same)

custom panel with emoji icon and colour

```xml
<ac:structured-macro ac:name="panel" ac:schema-version="1">
  <ac:parameter ac:name="panelIcon">:rocket:</ac:parameter>
  <ac:parameter ac:name="panelIconId">1f680</ac:parameter>
  <ac:parameter ac:name="panelIconText">🚀</ac:parameter>
  <ac:parameter ac:name="bgColor">#EAE6FF</ac:parameter>
  <ac:rich-text-body>
    <p>custom</p>
  </ac:rich-text-body>
</ac:structured-macro>
```

### custom-plain (same)

custom panel without icon

```xml
<ac:structured-macro ac:name="panel" ac:schema-version="1">
  <ac:parameter ac:name="bgColor">#E3FCEF</ac:parameter>
  <ac:rich-text-body>
    <p>custom, no icon</p>
  </ac:rich-text-body>
</ac:structured-macro>
```

### error (same)

error panel

```xml
<ac:adf-extension>
  <ac:adf-node type="panel">
    <ac:adf-attribute key="panel-type">error</ac:adf-attribute>
    <ac:adf-content>
      <p>error</p>
    </ac:adf-content>
  </ac:adf-node>
</ac:adf-extension>
```

### info (same)

info panel

```xml
<ac:structured-macro ac:name="info" ac:schema-version="1">
  <ac:rich-text-body>
    <p>info</p>
  </ac:rich-text-body>
</ac:structured-macro>
```

### note (same)

note panel

```xml
<ac:structured-macro ac:name="note" ac:schema-version="1">
  <ac:rich-text-body>
    <p>note</p>
  </ac:rich-text-body>
</ac:structured-macro>
```

### rich (same)

panel with list, task and code inside

```xml
<ac:structured-macro ac:name="info" ac:schema-version="1">
  <ac:rich-text-body>
    <ul>
      <li>
        <p>list</p>
      </li>
    </ul>
    <ac:task-list>
      <ac:task>
        <ac:task-id>6</ac:task-id>
        <ac:task-status>incomplete</ac:task-status>
        <ac:task-body>task</ac:task-body>
      </ac:task>
    </ac:task-list>
    <ac:structured-macro ac:name="code" ac:schema-version="1">
      <ac:plain-text-body>code in a panel</ac:plain-text-body>
    </ac:structured-macro>
  </ac:rich-text-body>
</ac:structured-macro>
```

### success (same)

success panel (tip macro)

```xml
<ac:structured-macro ac:name="tip" ac:schema-version="1">
  <ac:rich-text-body>
    <p>success</p>
  </ac:rich-text-body>
</ac:structured-macro>
```

### warning (same)

warning panel

```xml
<ac:structured-macro ac:name="warning" ac:schema-version="1">
  <ac:rich-text-body>
    <p>warning</p>
  </ac:rich-text-body>
</ac:structured-macro>
```

## paragraph

### aligned (same)

paragraph alignment: center and end

```xml
<p style="text-align: center;">Centered paragraph.</p>
<p style="text-align: right;">Right-aligned paragraph.</p>
```

### hard-break (same)

hardBreak inside a paragraph

```xml
<p>Line one<br/>line two<br/>line three.</p>
```

### indented (same)

indentation levels 1 to 3

```xml
<p style="margin-left: 30.0px;">Indented once.</p>
<p style="margin-left: 60.0px;">Indented twice.</p>
<p style="margin-left: 90.0px;">Indented three times.</p>
```

### placeholder (same)

placeholder text

```xml
<p>Placeholder: <ac:placeholder>Type something here</ac:placeholder></p>
```

### plain (same)

plain paragraph

```xml
<p>Plain paragraph.</p>
```

## rule

### hr (same)

horizontal rule

```xml
<hr/>
```

## status

### colours (same)

status in every colour

```xml
<p>
  <ac:structured-macro ac:name="status" ac:schema-version="1">
    <ac:parameter ac:name="title">NEUTRAL</ac:parameter>
  </ac:structured-macro>
  <ac:structured-macro ac:name="status" ac:schema-version="1">
    <ac:parameter ac:name="colour">Grey</ac:parameter>
    <ac:parameter ac:name="title">GREY</ac:parameter>
  </ac:structured-macro>
  <ac:structured-macro ac:name="status" ac:schema-version="1">
    <ac:parameter ac:name="colour">Red</ac:parameter>
    <ac:parameter ac:name="title">RED</ac:parameter>
  </ac:structured-macro>
  <ac:structured-macro ac:name="status" ac:schema-version="1">
    <ac:parameter ac:name="colour">Yellow</ac:parameter>
    <ac:parameter ac:name="title">YELLOW</ac:parameter>
  </ac:structured-macro>
  <ac:structured-macro ac:name="status" ac:schema-version="1">
    <ac:parameter ac:name="colour">Green</ac:parameter>
    <ac:parameter ac:name="title">GREEN</ac:parameter>
  </ac:structured-macro>
  <ac:structured-macro ac:name="status" ac:schema-version="1">
    <ac:parameter ac:name="colour">Blue</ac:parameter>
    <ac:parameter ac:name="title">BLUE</ac:parameter>
  </ac:structured-macro>
  <ac:structured-macro ac:name="status" ac:schema-version="1">
    <ac:parameter ac:name="colour">Purple</ac:parameter>
    <ac:parameter ac:name="title">PURPLE</ac:parameter>
  </ac:structured-macro>
</p>
```

### subtle (same)

subtle status

```xml
<p>
  <ac:structured-macro ac:name="status" ac:schema-version="1">
    <ac:parameter ac:name="colour">Green</ac:parameter>
    <ac:parameter ac:name="title">SUBTLE</ac:parameter>
    <ac:parameter ac:name="subtle">true</ac:parameter>
  </ac:structured-macro>
</p>
```

## table

### cell-colours (same)

cell background colours

```xml
<table data-layout="default">
  <tbody>
    <tr>
      <td data-highlight-colour="#deebff">
        <p>blue</p>
      </td>
      <td data-highlight-colour="#e3fcef">
        <p>green</p>
      </td>
      <td data-highlight-colour="#ffebe6">
        <p>red</p>
      </td>
    </tr>
  </tbody>
</table>
```

### full-width-rich (same)

full-width table with rich cells and nestedExpand

```xml
<table data-layout="full-width">
  <tbody>
    <tr>
      <th>
        <p>Rich cell</p>
      </th>
    </tr>
    <tr>
      <td>
        <ul>
          <li>
            <p>bullet in cell</p>
          </li>
        </ul>
        <ac:task-list>
          <ac:task>
            <ac:task-id>71</ac:task-id>
            <ac:task-status>complete</ac:task-status>
            <ac:task-body>task in cell</ac:task-body>
          </ac:task>
        </ac:task-list>
        <ac:structured-macro ac:name="code" ac:schema-version="1">
          <ac:plain-text-body>code in cell</ac:plain-text-body>
        </ac:structured-macro>
        <ac:structured-macro ac:name="info" ac:schema-version="1">
          <ac:rich-text-body>
            <p>panel in cell</p>
          </ac:rich-text-body>
        </ac:structured-macro>
        <ac:structured-macro ac:name="expand" ac:schema-version="1">
          <ac:parameter ac:name="title">nestedExpand in cell</ac:parameter>
          <ac:rich-text-body>
            <p>nested expand content</p>
          </ac:rich-text-body>
        </ac:structured-macro>
      </td>
    </tr>
  </tbody>
</table>
```

### header-column (same)

header column

```xml
<table data-layout="default">
  <tbody>
    <tr>
      <th>
        <p>Row</p>
      </th>
      <td>
        <p>value</p>
      </td>
    </tr>
    <tr>
      <th>
        <p>Row 2</p>
      </th>
      <td>
        <p>value</p>
      </td>
    </tr>
  </tbody>
</table>
```

### header-row (same)

header row

```xml
<table data-layout="default">
  <tbody>
    <tr>
      <th>
        <p>A</p>
      </th>
      <th>
        <p>B</p>
      </th>
    </tr>
    <tr>
      <td>
        <p>1</p>
      </td>
      <td>
        <p>2</p>
      </td>
    </tr>
  </tbody>
</table>
```

### spans-widths (same)

wide layout, column widths, colspan and rowspan

```xml
<table data-layout="wide">
  <colgroup>
    <col style="width: 120.0px;"/>
    <col style="width: 360.0px;"/>
    <col style="width: 240.0px;"/>
  </colgroup>
  <tbody>
    <tr>
      <th>
        <p>A</p>
      </th>
      <th colspan="2">
        <p>colspan 2</p>
      </th>
    </tr>
    <tr>
      <td rowspan="2">
        <p>rowspan 2</p>
      </td>
      <td>
        <p>a</p>
      </td>
      <td>
        <p>b</p>
      </td>
    </tr>
    <tr>
      <td colspan="2">
        <p>colspan 2</p>
      </td>
    </tr>
  </tbody>
</table>
```

## taskList

### due-date (same)

task with a due date and marks

```xml
<ac:task-list>
  <ac:task>
    <ac:task-id>3</ac:task-id>
    <ac:task-status>incomplete</ac:task-status>
    <ac:task-body>Due <time datetime="2026-10-01"/> with <strong>marks</strong></ac:task-body>
  </ac:task>
</ac:task-list>
```

### nested (same)

nested task list

```xml
<ac:task-list>
  <ac:task>
    <ac:task-id>4</ac:task-id>
    <ac:task-status>incomplete</ac:task-status>
    <ac:task-body>Parent task</ac:task-body>
    <ac:task-list>
      <ac:task>
        <ac:task-id>5</ac:task-id>
        <ac:task-status>incomplete</ac:task-status>
        <ac:task-body>Nested task</ac:task-body>
      </ac:task>
    </ac:task-list>
  </ac:task>
</ac:task-list>
```

### states (same)

open and done tasks

```xml
<ac:task-list>
  <ac:task>
    <ac:task-id>1</ac:task-id>
    <ac:task-status>incomplete</ac:task-status>
    <ac:task-body>Open task</ac:task-body>
  </ac:task>
  <ac:task>
    <ac:task-id>2</ac:task-id>
    <ac:task-status>complete</ac:task-status>
    <ac:task-body>Done task</ac:task-body>
  </ac:task>
</ac:task-list>
```

## Rejected forms

Confluence drops or rewrites these; do not write them.

### code-breakout

code blocks and expands cannot be made wide or full-width through storage format

```xml
<ac:structured-macro ac:name="code" ac:schema-version="1" ac:breakout-mode="wide">
  <ac:plain-text-body>wide</ac:plain-text-body>
</ac:structured-macro>
```

### expand-breakout

code blocks and expands cannot be made wide or full-width through storage format

```xml
<ac:structured-macro ac:name="expand" ac:schema-version="1" ac:breakout-mode="wide">
  <ac:parameter ac:name="title">wide</ac:parameter>
  <ac:rich-text-body>
    <p>wide</p>
  </ac:rich-text-body>
</ac:structured-macro>
```

### highlight-span

for a text highlight write <span style="background-color: rgb(…)"> (see marks/background-color)

```xml
<p>
  <span data-highlight-colour="#f8e6a0">yellow</span>
</p>
```

### table-number-column

the numbered first column of a table cannot be set through storage format

```xml
<table data-layout="default" data-number-column="true">
  <tbody>
    <tr>
      <td>
        <p>1</p>
      </td>
    </tr>
  </tbody>
</table>
```
