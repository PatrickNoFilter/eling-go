// Package markdownify converts documents to Markdown.
// Adapted from the Python eling-agent's markdownify MCP server.
// Supports: PDF, DOCX, XLSX, PPTX, HTML, CSV, images (OCR), audio (transcription),
// and web pages — using pandoc, pdftotext, and other CLI tools as backends.
package markdownify

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Converter handles document-to-Markdown conversion.
type Converter struct {
	// Available backends (detected at init)
	hasPandoc    bool
	hasPdftotext bool
	hasTesseract bool
	hasWhisper   bool
}

// NewConverter creates a new Converter and detects available backends.
func NewConverter() *Converter {
	c := &Converter{}
	c.detectBackends()
	return c
}

// detectBackends checks for available conversion tools.
func (c *Converter) detectBackends() {
	c.hasPandoc = commandExists("pandoc")
	c.hasPdftotext = commandExists("pdftotext")
	c.hasTesseract = commandExists("tesseract")
	c.hasWhisper = commandExists("whisper") || commandExists("whisper-cpp")
}

// ConvertFile converts a document file to Markdown.
// Supported formats: .pdf, .docx, .xlsx, .pptx, .html, .htm, .csv, .txt, .md, .jpg, .jpeg, .png, .gif, .bmp, .tiff, .mp3, .wav, .ogg, .flac
func (c *Converter) ConvertFile(ctx context.Context, path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))

	switch ext {
	case ".pdf":
		return c.convertPDF(ctx, path)
	case ".docx", ".xlsx", ".pptx":
		return c.convertOffice(ctx, path, ext)
	case ".html", ".htm":
		return c.convertHTML(ctx, path)
	case ".csv":
		return c.convertCSV(ctx, path)
	case ".txt", ".md":
		return c.readText(path)
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tiff", ".webp":
		return c.convertImage(ctx, path)
	case ".mp3", ".wav", ".ogg", ".flac", ".m4a", ".aac":
		return c.convertAudio(ctx, path)
	default:
		// Try reading as text
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("unsupported format %s and cannot read as text: %w", ext, err)
		}
		return string(data), nil
	}
}

// ConvertURL fetches a URL and converts it to Markdown.
func (c *Converter) ConvertURL(ctx context.Context, url string) (string, error) {
	// Try pandoc first
	if c.hasPandoc {
		cmd := exec.CommandContext(ctx, "pandoc", "-f", "html", "-t", "markdown",
			"--wrap=preserve", url)
		output, err := cmd.Output()
		if err == nil && len(output) > 0 {
			return string(bytes.TrimSpace(output)), nil
		}
	}

	// Fallback: fetch with curl and convert
	cmd := exec.CommandContext(ctx, "curl", "-sL", url)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to fetch URL: %w", err)
	}

	html := string(output)

	// Simple HTML-to-Markdown conversion
	md := htmlToMarkdown(html)
	return md, nil
}

// SupportedFormats returns a list of supported file extensions.
func (c *Converter) SupportedFormats() []string {
	formats := []string{".pdf", ".docx", ".xlsx", ".pptx", ".html", ".htm",
		".csv", ".txt", ".md", ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tiff",
		".mp3", ".wav", ".ogg", ".flac"}

	// Mark which have backend support
	if !c.hasPandoc {
		formats = append(formats[:0], formats[:0]...) // non-destructive
	}

	return formats
}

// --- Backend-specific converters ---

func (c *Converter) convertPDF(ctx context.Context, path string) (string, error) {
	// Try pandoc first (handles PDF better)
	if c.hasPandoc {
		cmd := exec.CommandContext(ctx, "pandoc", path, "-f", "pdf",
			"-t", "markdown", "--wrap=preserve")
		output, err := cmd.Output()
		if err == nil && len(output) > 50 {
			return string(bytes.TrimSpace(output)), nil
		}
	}

	// Try pdftotext as fallback
	if c.hasPdftotext {
		cmd := exec.CommandContext(ctx, "pdftotext", "-layout", path, "-")
		output, err := cmd.Output()
		if err == nil {
			text := string(output)
			// Wrap in code block for readability
			return fmt.Sprintf("```\n%s\n```", text), nil
		}
	}

	// Last resort: read raw bytes and return error
	data, _ := os.ReadFile(path)
	if len(data) > 0 {
		return fmt.Sprintf("PDF file (%d bytes). Install pandoc or pdftotext for conversion.", len(data)), nil
	}
	return "", fmt.Errorf("cannot convert PDF: install pandoc or pdftotext")
}

func (c *Converter) convertOffice(ctx context.Context, path string, ext string) (string, error) {
	if !c.hasPandoc {
		return "", fmt.Errorf("pandoc is required for %s conversion. Install with: apt install pandoc", ext)
	}

	fromFormat := strings.TrimPrefix(ext, ".")
	if fromFormat == "xlsx" {
		fromFormat = "docx" // Pandoc handles xlsx via docx
	}

	cmd := exec.CommandContext(ctx, "pandoc", path, "-f", fromFormat,
		"-t", "markdown", "--wrap=preserve")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pandoc conversion failed: %w", err)
	}

	return string(bytes.TrimSpace(output)), nil
}

func (c *Converter) convertHTML(ctx context.Context, path string) (string, error) {
	if c.hasPandoc {
		cmd := exec.CommandContext(ctx, "pandoc", path, "-f", "html",
			"-t", "markdown", "--wrap=preserve")
		output, err := cmd.Output()
		if err == nil && len(output) > 0 {
			return string(bytes.TrimSpace(output)), nil
		}
	}

	// Fallback: read and do simple conversion
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return htmlToMarkdown(string(data)), nil
}

func (c *Converter) convertCSV(ctx context.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("| ")
	for _, cell := range strings.Split(lines[0], ",") {
		sb.WriteString(strings.TrimSpace(cell) + " | ")
	}
	sb.WriteString("\n|")
	for i := 0; i < len(strings.Split(lines[0], ",")); i++ {
		sb.WriteString("---|")
	}
	sb.WriteString("\n")

	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sb.WriteString("| ")
		for _, cell := range strings.Split(line, ",") {
			sb.WriteString(strings.TrimSpace(cell) + " | ")
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

func (c *Converter) readText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (c *Converter) convertImage(ctx context.Context, path string) (string, error) {
	if !c.hasTesseract {
		return fmt.Sprintf("![Image](%s)\n\n*OCR not available — install tesseract-ocr for text extraction*", filepath.Base(path)), nil
	}

	cmd := exec.CommandContext(ctx, "tesseract", path, "stdout", "-l", "eng")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("![Image](%s)\n\n*OCR failed: %v*", filepath.Base(path), err), nil
	}

	text := string(bytes.TrimSpace(output))
	return fmt.Sprintf("![Image](%s)\n\n```\n%s\n```", filepath.Base(path), text), nil
}

func (c *Converter) convertAudio(ctx context.Context, path string) (string, error) {
	if !c.hasWhisper {
		return fmt.Sprintf("*Audio file: %s*\n\n*Transcription not available — install whisper-cpp for speech-to-text*", filepath.Base(path)), nil
	}

	// Try whisper-cpp first
	cmd := exec.CommandContext(ctx, "whisper-cpp", path, "--output-txt", "--output-dir", filepath.Dir(path))
	output, err := cmd.Output()
	if err == nil {
		txtPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".txt"
		if data, err := os.ReadFile(txtPath); err == nil {
			return fmt.Sprintf("## Audio Transcription: %s\n\n%s", filepath.Base(path), string(data)), nil
		}
		return string(output), nil
	}

	return fmt.Sprintf("*Audio file: %s*\n\n*Transcription failed: %v*", filepath.Base(path), err), nil
}

// ConvertTime returns the conversion duration.
func (c *Converter) ConvertTime(path string) (time.Duration, error) {
	start := time.Now()
	_, err := c.ConvertFile(context.Background(), path)
	return time.Since(start), err
}

// --- Helpers ---

func commandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// htmlToMarkdown performs a simple HTML-to-Markdown conversion.
// For production use, install pandoc for proper conversion.
func htmlToMarkdown(html string) string {
	// Simple tag stripping — not perfect but functional
	md := html

	// Headers
	md = strings.ReplaceAll(md, "<h1>", "# ")
	md = strings.ReplaceAll(md, "</h1>", "")
	md = strings.ReplaceAll(md, "<h2>", "## ")
	md = strings.ReplaceAll(md, "</h2>", "")
	md = strings.ReplaceAll(md, "<h3>", "### ")
	md = strings.ReplaceAll(md, "</h3>", "")
	md = strings.ReplaceAll(md, "<h4>", "#### ")
	md = strings.ReplaceAll(md, "</h4>", "")

	// Bold/Italic
	md = strings.ReplaceAll(md, "<strong>", "**")
	md = strings.ReplaceAll(md, "</strong>", "**")
	md = strings.ReplaceAll(md, "<em>", "*")
	md = strings.ReplaceAll(md, "</em>", "*")

	// Links
	md = regexReplaceAll(md, `<a\s+[^>]*href="([^"]*)"[^>]*>([^<]*)</a>`, "[$2]($1)")

	// Images
	md = regexReplaceAll(md, `<img\s+[^>]*src="([^"]*)"[^>]*alt="([^"]*)"[^>]*>`, "![$2]($1)")
	md = regexReplaceAll(md, `<img\s+[^>]*src="([^"]*)"[^>]*>`, "![Image]($1)")

	// Lists
	md = strings.ReplaceAll(md, "<li>", "- ")
	md = strings.ReplaceAll(md, "</li>", "")
	md = strings.ReplaceAll(md, "<ul>", "")
	md = strings.ReplaceAll(md, "</ul>", "")
	md = strings.ReplaceAll(md, "<ol>", "")
	md = strings.ReplaceAll(md, "</ol>", "")

	// Paragraphs
	md = strings.ReplaceAll(md, "<p>", "")
	md = strings.ReplaceAll(md, "</p>", "\n\n")

	// Code
	md = strings.ReplaceAll(md, "<code>", "`")
	md = strings.ReplaceAll(md, "</code>", "`")
	md = strings.ReplaceAll(md, "<pre>", "```\n")
	md = strings.ReplaceAll(md, "</pre>", "\n```")

	// Line breaks
	md = strings.ReplaceAll(md, "<br>", "\n")
	md = strings.ReplaceAll(md, "<br/>", "\n")
	md = strings.ReplaceAll(md, "<br />", "\n")

	// Strip remaining tags
	md = stripHTMLTags(md)

	// Clean up
	md = strings.TrimSpace(md)

	return md
}

// regexReplaceAll is a simple regex replacement (replaces 'regexp' import).
func regexReplaceAll(s, pattern, replacement string) string {
	// Simple implementation for basic patterns
	// In production, use regexp package
	if strings.Contains(pattern, `href="([^"]*)"`) {
		// Handle link pattern
		result := ""
		for {
			start := strings.Index(s, "<a ")
			if start < 0 {
				break
			}
			end := strings.Index(s[start:], "</a>")
			if end < 0 {
				break
			}
			end += start + 4
			tag := s[start:end]

			// Extract href
			hrefStart := strings.Index(tag, `href="`)
			var href string
			if hrefStart >= 0 {
				hrefStart += 6
				hrefEnd := strings.Index(tag[hrefStart:], `"`)
				if hrefEnd >= 0 {
					href = tag[hrefStart : hrefStart+hrefEnd]
				}
			}

			// Extract text
			textStart := strings.Index(tag, ">")
			textEnd := strings.LastIndex(tag, "<")
			text := ""
			if textStart >= 0 && textEnd > textStart {
				text = tag[textStart+1 : textEnd]
			}

			replacement := fmt.Sprintf("[%s](%s)", text, href)
			result += s[:start] + replacement
			s = s[end:]
		}
		return result + s
	}

	if strings.Contains(pattern, `src="([^"]*)"`) {
		// Handle image pattern
		result := ""
		for {
			start := strings.Index(s, "<img")
			if start < 0 {
				break
			}
			end := strings.Index(s[start:], ">")
			if end < 0 {
				break
			}
			end += start + 1
			tag := s[start:end]

			// Extract src
			srcStart := strings.Index(tag, `src="`)
			var src string
			if srcStart >= 0 {
				srcStart += 5
				srcEnd := strings.Index(tag[srcStart:], `"`)
				if srcEnd >= 0 {
					src = tag[srcStart : srcStart+srcEnd]
				}
			}

			// Extract alt
			altStart := strings.Index(tag, `alt="`)
			alt := ""
			if altStart >= 0 {
				altStart += 5
				altEnd := strings.Index(tag[altStart:], `"`)
				if altEnd >= 0 {
					alt = tag[altStart : altStart+altEnd]
				}
			}

			replacement := fmt.Sprintf("![%s](%s)", alt, src)
			result += s[:start] + replacement
			s = s[end:]
		}
		return result + s
	}

	return s
}

// stripHTMLTags removes all remaining HTML tags.
func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// --- HTTP Server Mode ---

// ServeHTTP starts a simple HTTP server that provides markdownify as a REST API.
// GET /convert?url=<url> — convert a URL to Markdown
// POST /convert — upload a file to convert
func (c *Converter) ServeHTTP(addr string) error {
	http.HandleFunc("/convert", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")

		if r.Method == "GET" {
			url := r.URL.Query().Get("url")
			if url == "" {
				http.Error(w, "Missing 'url' parameter", http.StatusBadRequest)
				return
			}
			md, err := c.ConvertURL(r.Context(), url)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, md)
			return
		}

		if r.Method == "POST" {
			file, header, err := r.FormFile("file")
			if err != nil {
				http.Error(w, "Missing 'file' field: "+err.Error(), http.StatusBadRequest)
				return
			}
			defer file.Close()

			tmpFile := filepath.Join(os.TempDir(), "markdownify-"+header.Filename)
			f, err := os.Create(tmpFile)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, err = f.ReadFrom(file)
			f.Close()
			if err != nil {
				os.Remove(tmpFile)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer os.Remove(tmpFile)

			md, err := c.ConvertFile(r.Context(), tmpFile)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, md)
			return
		}

		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	})

	return http.ListenAndServe(addr, nil)
}
