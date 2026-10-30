package zorxauth

// Validation of uploaded login page resources: format checks, and SVG
// screening for active and external content.

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"image"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// svgForbiddenElements lists SVG elements that can run script or embed a
// document (lowercase, without namespace).
var svgForbiddenElements = map[string]bool{
	"script":        true, //Direct script execution
	"foreignobject": true, //Embeds arbitrary HTML, including <script>
	"iframe":        true,
	"embed":         true,
	"object":        true,
	"audio":         true,
	"video":         true,
	"handler":       true, // SVG Tiny event handler element
	"animate":       true, // can animate an attribute into an href or event handler
	"set":           true, //Same, via attributeName
}

// svgAllowedURLPrefixes are the allowed href and src targets: document
// fragments and inline raster data.
var svgAllowedURLPrefixes = []string{"#", "data:image/png", "data:image/jpeg", "data:image/gif", "data:image/webp"}

// validateUploadContent checks that content is the format claimed by ext and
// safe to serve, and the aspect ratio of spec.
func validateUploadContent(content []byte, ext string, spec *AssetSpec) error {
	switch ext {
	case ".svg":
		return validateSVGContent(content)
	case ".png", ".jpg", ".jpeg", ".webp":
		return validateRasterContent(content, ext, spec)
	}
	return errors.New("Unsupported file type. Accepted formats: SVG, PNG, JPG, WEBP")
}

/* ── SVG ─────────────────────────────────────────────────────────────── */

// validateSVGContent rejects SVG uploads carrying active or external content.
func validateSVGContent(content []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	// Named HTML entities are allowed; entity declarations are rejected below
	decoder.Entity = xml.HTMLEntity
	decoder.Strict = true

	sawRoot := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("SVG file is not well formed XML: " + err.Error())
		}

		switch t := token.(type) {
		case xml.ProcInst:
			// Only the xml declaration is allowed
			if !strings.EqualFold(t.Target, "xml") {
				return errors.New("SVG file contains an unsupported processing instruction: <?" + t.Target + "?>")
			}

		case xml.Directive:
			// Rejects entity declarations
			directive := strings.ToUpper(string(t))
			if strings.Contains(directive, "ENTITY") {
				return errors.New("SVG file declares XML entities, which are not allowed")
			}

		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if !sawRoot {
				if name != "svg" {
					return errors.New("SVG file root element must be <svg>")
				}
				sawRoot = true
			}

			if svgForbiddenElements[name] {
				return errors.New("SVG file contains a disallowed <" + name + "> element. Remove any scripts, animations or embedded documents from the artwork.")
			}

			if err := validateSVGAttributes(name, t.Attr); err != nil {
				return err
			}
		}
	}

	if !sawRoot {
		return errors.New("SVG file does not contain an <svg> element")
	}

	return nil
}

// validateSVGAttributes screens a single element's attributes for event
// handlers, script URLs and remote references.
func validateSVGAttributes(elementName string, attrs []xml.Attr) error {
	for _, attr := range attrs {
		name := strings.ToLower(attr.Name.Local)
		value := strings.TrimSpace(attr.Value)

		//Event handlers: onload, onclick, onmouseover, ...
		if strings.HasPrefix(name, "on") {
			return errors.New("SVG file contains an event handler attribute (" + attr.Name.Local + "). Remove any interactivity from the artwork.")
		}

		//Script URLs in any attribute, including style
		if containsScriptURL(value) {
			return errors.New("SVG file contains a script URL in the " + attr.Name.Local + " attribute")
		}

		//Reference attributes may only point inside the document or at inline image data
		if name == "href" || name == "src" {
			if !isAllowedSVGReference(value) {
				return errors.New("SVG file references an external resource (" + attr.Name.Local + "). Embed the artwork instead of linking to it.")
			}
		}

		//Inline CSS may not import remote stylesheets
		if name == "style" && strings.Contains(strings.ToLower(value), "@import") {
			return errors.New("SVG file imports an external stylesheet, which is not allowed")
		}
	}

	return nil
}

// containsScriptURL reports whether value, with whitespace and control
// characters removed, contains a script-capable scheme.
func containsScriptURL(value string) bool {
	var sb strings.Builder
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			continue
		}
		sb.WriteRune(unicode.ToLower(r))
	}
	normalized := sb.String()

	return strings.Contains(normalized, "javascript:") ||
		strings.Contains(normalized, "vbscript:") ||
		strings.Contains(normalized, "data:text/html") ||
		strings.Contains(normalized, "data:image/svg+xml")
}

// isAllowedSVGReference reports whether a href / src value is a same document
// fragment or an inline raster data URI.
func isAllowedSVGReference(value string) bool {
	if value == "" {
		return true
	}

	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range svgAllowedURLPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}

	return false
}

/* ── Raster ──────────────────────────────────────────────────────────── */

// validateRasterContent verifies the file signature matches the extension, then
// enforces the aspect ratio of the resource being replaced.
func validateRasterContent(content []byte, ext string, spec *AssetSpec) error {
	if err := checkRasterSignature(content, ext); err != nil {
		return err
	}

	width, height, err := rasterDimensions(content, ext)
	if err != nil {
		return err
	}

	return checkAspectRatio(width, height, spec)
}

// checkRasterSignature checks that the file signature matches the extension.
func checkRasterSignature(content []byte, ext string) error {
	valid := false
	switch ext {
	case ".png":
		valid = bytes.HasPrefix(content, []byte("\x89PNG\r\n\x1a\n"))
	case ".jpg", ".jpeg":
		valid = bytes.HasPrefix(content, []byte("\xff\xd8\xff"))
	case ".webp":
		valid = len(content) >= 12 && bytes.HasPrefix(content, []byte("RIFF")) && bytes.Equal(content[8:12], []byte("WEBP"))
	}

	if !valid {
		return errors.New("File content does not match its " + ext + " extension. The file may be corrupted or renamed.")
	}

	return nil
}

// rasterDimensions returns the pixel dimensions of a validated raster upload
func rasterDimensions(content []byte, ext string) (int, int, error) {
	if ext == ".webp" {
		return webpDimensions(content)
	}

	config, _, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return 0, 0, errors.New("Could not read the image dimensions. Try re-exporting the file as PNG.")
	}

	return config.Width, config.Height, nil
}

// webpDimensions reads the canvas size of a WEBP file.
func webpDimensions(content []byte) (int, int, error) {
	//Chunks start after the 12 byte RIFF header: 4 byte FourCC + 4 byte size
	if len(content) < 30 {
		return 0, 0, errors.New("WEBP file is truncated")
	}

	switch string(content[12:16]) {
	case "VP8X":
		//Extended format: 24 bit little endian canvas width-1 / height-1 at offset 24
		width := int(content[24]) | int(content[25])<<8 | int(content[26])<<16
		height := int(content[27]) | int(content[28])<<8 | int(content[29])<<16
		return width + 1, height + 1, nil

	case "VP8 ":
		//Lossy: 3 byte start code 0x9d012a, then 16 bit width / height (14 bit each)
		if len(content) < 30 || content[23] != 0x9d || content[24] != 0x01 || content[25] != 0x2a {
			return 0, 0, errors.New("WEBP file has an unsupported lossy header")
		}
		width := int(binary.LittleEndian.Uint16(content[26:28]) & 0x3fff)
		height := int(binary.LittleEndian.Uint16(content[28:30]) & 0x3fff)
		return width, height, nil

	case "VP8L":
		//Lossless: signature byte 0x2f, then 14 bit width-1 and 14 bit height-1
		if content[20] != 0x2f {
			return 0, 0, errors.New("WEBP file has an unsupported lossless header")
		}
		bits := binary.LittleEndian.Uint32(content[21:25])
		width := int(bits&0x3fff) + 1
		height := int((bits>>14)&0x3fff) + 1
		return width, height, nil
	}

	return 0, 0, errors.New("Could not read the WEBP image dimensions. Try re-exporting the file as PNG.")
}

// checkAspectRatio checks an image's ratio against the default resource's.
func checkAspectRatio(width int, height int, spec *AssetSpec) error {
	if width <= 0 || height <= 0 {
		return errors.New("Uploaded image has invalid dimensions")
	}

	expected := float64(spec.Width) / float64(spec.Height)
	actual := float64(width) / float64(height)
	if math.Abs(actual-expected)/expected > BRANDING_ASPECT_TOLERANCE {
		return errors.New("Image aspect ratio must match the default resource (" +
			strconv.Itoa(spec.Width) + " × " + strconv.Itoa(spec.Height) + "). Uploaded image is " +
			strconv.Itoa(width) + " × " + strconv.Itoa(height) +
			". Use the scaling tool to adjust it before uploading.")
	}

	return nil
}

/* ── Text ────────────────────────────────────────────────────────────── */

// sanitizeBrandingText strips control characters from a label.
func sanitizeBrandingText(value string) string {
	var sb strings.Builder
	for _, r := range value {
		if r == '\t' || r == '\n' || r == '\r' {
			sb.WriteRune(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		sb.WriteRune(r)
	}

	return strings.TrimSpace(sb.String())
}
