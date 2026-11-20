package examfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// markdown is CommonMark without extensions. It is never configured with
// html.WithUnsafe, so raw HTML is omitted and dangerous link URLs are dropped.
var markdown = goldmark.New(goldmark.WithRendererOptions(html.WithHardWraps()))

var imageExts = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// renderer turns block text into HTML and collects the images it references.
type renderer struct {
	upload    Upload
	mediaBase string
	images    []Image
	seen      map[string]bool
	paths     map[string]string // cleaned upload path → Image.Name; nil until an image resolves
}

func newRenderer(u Upload, mediaBase string) *renderer {
	return &renderer{upload: u, mediaBase: mediaBase, seen: map[string]bool{}}
}

// place names the block being rendered in image errors: question n or
// section n.
type place struct {
	section bool
	n       int // 1-based number
}

// imageError is the image rule error of code (for a question) or
// sectionCode (for a section) about ref in block at.
func (at place) imageError(code, sectionCode Code, ref string) error {
	if at.section {
		code = sectionCode
	}
	return NewError(code, 0, at.n, ref)
}

// render converts Markdown to HTML, pointing every image at mediaBase.
// at names the block in image errors.
func (r *renderer) render(src string, at place) (template.HTML, error) {
	source := []byte(src)
	doc := markdown.Parser().Parse(text.NewReader(source))
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		img, ok := n.(*ast.Image)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		name, err := r.resolveImage(string(img.Destination), at)
		if err != nil {
			return ast.WalkStop, err
		}
		img.Destination = []byte(r.mediaBase + "/" + name)
		return ast.WalkContinue, nil
	})
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := markdown.Renderer().Render(&buf, source, doc); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// resolveImage finds ref inside the upload, relative to the exam text's
// directory, and returns the image's content-addressed Name.
func (r *renderer) resolveImage(ref string, at place) (string, error) {
	outside := func() error {
		return at.imageError(CodeImageOutsideQuestion, CodeImageOutsideSection, ref)
	}
	decoded, err := url.PathUnescape(ref)
	if err != nil {
		return "", outside()
	}
	if parsed, err := url.Parse(ref); err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return "", outside()
	}
	if strings.HasPrefix(decoded, "/") || strings.HasPrefix(decoded, `\`) {
		return "", outside()
	}
	p := path.Clean(path.Join(r.upload.Dir, decoded))
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", outside()
	}
	data, ok := r.upload.Files[p]
	if !ok {
		return "", at.imageError(CodeImageMissingQuestion, CodeImageMissingSection, ref)
	}
	name, ok := ImageName(data)
	if !ok {
		return "", at.imageError(CodeImageTypeQuestion, CodeImageTypeSection, ref)
	}
	if r.paths == nil {
		r.paths = map[string]string{}
	}
	r.paths[p] = name
	if !r.seen[name] {
		r.seen[name] = true
		r.images = append(r.images, Image{Name: name, Data: data})
	}
	return name, nil
}

// ImageName returns the content-addressed file name of an accepted image
// (PNG, JPEG, GIF or WebP by content): the first 32 hex characters of its
// SHA-256 plus the type's extension. ok is false for any other content.
func ImageName(data []byte) (name string, ok bool) {
	ext, ok := imageExts[http.DetectContentType(data)]
	if !ok {
		return "", false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:32] + ext, true
}
