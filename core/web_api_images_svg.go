//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/xml"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var webImageSVGLocalID = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$`)
var webImageSVGURL = regexp.MustCompile(`(?i)url\(\s*["']?#([A-Za-z_][A-Za-z0-9_.:-]{0,127})["']?\s*\)`)
var webImageSVGFunction = regexp.MustCompile(`(?i)([a-z_-][a-z0-9_-]*)\s*\(`)
var webImageSVGURLFunction = regexp.MustCompile(`(?i)url\s*\(`)

func webImageSVGDimension(value string) (float64, bool) {
	value = strings.TrimSuffix(strings.TrimSpace(value), "px")
	number, err := strconv.ParseFloat(value, 64)
	return number, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0) && number > 0 && number <= 16384
}

func webImageSVGStyles(value string) ([]string, bool) {
	lower := strings.ToLower(value)
	if strings.ContainsAny(lower, "\\@<>") || strings.Contains(lower, "/*") || strings.Contains(lower, "expression") || strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:") || strings.Contains(lower, "http:") || strings.Contains(lower, "https:") || strings.Contains(lower, "//") {
		return nil, false
	}
	matches := webImageSVGURL.FindAllStringSubmatch(value, -1)
	cleaned := webImageSVGURL.ReplaceAllString(value, "")
	if webImageSVGURLFunction.MatchString(cleaned) {
		return nil, false
	}
	for _, function := range webImageSVGFunction.FindAllStringSubmatch(cleaned, -1) {
		switch strings.ToLower(function[1]) {
		case "rgb", "rgba", "hsl", "hsla", "matrix", "translate", "scale", "rotate", "skewx", "skewy":
		default:
			return nil, false
		}
	}
	refs := []string{}
	for _, match := range matches {
		refs = append(refs, match[1])
	}
	return refs, true
}

func webImageValidateSVG(data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	allowed := map[string]bool{}
	for _, name := range strings.Fields("svg g defs path circle ellipse rect line polyline polygon linearGradient radialGradient stop clipPath mask pattern symbol use title desc text tspan style") {
		allowed[name] = true
	}
	attributes := map[string]bool{}
	for _, name := range strings.Fields("id version width height viewBox preserveAspectRatio x y x1 x2 y1 y2 cx cy r rx ry fx fy fr d points transform fill fill-rule fill-opacity stroke stroke-width stroke-linecap stroke-linejoin stroke-miterlimit stroke-dasharray stroke-dashoffset stroke-opacity opacity clip-path clip-rule mask maskUnits maskContentUnits gradientUnits gradientTransform spreadMethod offset stop-color stop-opacity patternUnits patternContentUnits patternTransform marker-start marker-mid marker-end color style class href xmlns space role aria-label aria-hidden focusable font-family font-size font-weight text-anchor dominant-baseline dx dy vector-effect") {
		attributes[name] = true
	}
	type frame struct{ name, owner, style string }
	stack := []frame{}
	ids := map[string]bool{}
	edges := map[string][]string{}
	refs := []string{}
	nodes, uses, pathBytes, styleBytes, textBytes := 0, 0, 0, 0, 0
	rootSeen, rootClosed := false, false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return webImageInvalid()
		}
		switch v := token.(type) {
		case xml.Directive:
			return webImageInvalid()
		case xml.ProcInst:
			if v.Target != "xml" || rootSeen {
				return webImageInvalid()
			}
		case xml.StartElement:
			nodes++
			if rootClosed || nodes > 20000 || len(stack) >= 64 || len(v.Attr) > 64 || !allowed[v.Name.Local] || v.Name.Space != "" && v.Name.Space != "http://www.w3.org/2000/svg" {
				return webImageInvalid()
			}
			if !rootSeen {
				if v.Name.Local != "svg" {
					return webImageInvalid()
				}
				rootSeen = true
			}
			owner := ""
			if len(stack) > 0 {
				owner = stack[len(stack)-1].owner
			}
			values := map[string]string{}
			localRefs := []string{}
			for _, attr := range v.Attr {
				name := attr.Name.Local
				if _, duplicate := values[name]; duplicate || strings.HasPrefix(strings.ToLower(name), "on") || len(attr.Value) > 65536 {
					return webImageInvalid()
				}
				values[name] = attr.Value
				if name == "xmlns" || attr.Name.Space == "xmlns" {
					if attr.Value != "http://www.w3.org/2000/svg" && attr.Value != "http://www.w3.org/1999/xlink" {
						return webImageInvalid()
					}
					continue
				}
				if !attributes[name] {
					return webImageInvalid()
				}
				if attr.Name.Space != "" && !(attr.Name.Space == "http://www.w3.org/1999/xlink" && name == "href") && !(attr.Name.Space == "http://www.w3.org/XML/1998/namespace" && name == "space") {
					return webImageInvalid()
				}
				if name == "href" {
					if !strings.HasPrefix(attr.Value, "#") || !webImageSVGLocalID.MatchString(attr.Value[1:]) {
						return webImageInvalid()
					}
					localRefs = append(localRefs, attr.Value[1:])
				} else if name != "d" && name != "points" && name != "aria-label" {
					found, ok := webImageSVGStyles(attr.Value)
					if !ok {
						return webImageInvalid()
					}
					localRefs = append(localRefs, found...)
				}
				if name == "d" || name == "points" {
					pathBytes += len(attr.Value)
				}
			}
			if id := values["id"]; id != "" {
				if !webImageSVGLocalID.MatchString(id) || ids[id] {
					return webImageInvalid()
				}
				ids[id] = true
				if owner != "" {
					edges[owner] = append(edges[owner], id)
				}
				owner = id
			}
			if v.Name.Local == "use" {
				uses++
			}
			if uses > 256 || pathBytes > 2<<20 || len(refs)+len(localRefs) > 4096 {
				return webImageInvalid()
			}
			refs = append(refs, localRefs...)
			if owner != "" {
				edges[owner] = append(edges[owner], localRefs...)
			}
			if len(stack) == 0 {
				width, height := float64(300), float64(150)
				if viewBox := strings.Fields(strings.ReplaceAll(values["viewBox"], ",", " ")); len(viewBox) > 0 {
					if len(viewBox) != 4 {
						return webImageInvalid()
					}
					var ok bool
					width, ok = webImageSVGDimension(viewBox[2])
					if !ok {
						return webImageInvalid()
					}
					height, ok = webImageSVGDimension(viewBox[3])
					if !ok {
						return webImageInvalid()
					}
				}
				for name, target := range map[string]*float64{"width": &width, "height": &height} {
					if values[name] != "" {
						value, ok := webImageSVGDimension(values[name])
						if !ok {
							return webImageInvalid()
						}
						*target = value
					}
				}
				if width*height > webImageMaxPixels {
					return webImageInvalid()
				}
			}
			stack = append(stack, frame{name: v.Name.Local, owner: owner})
		case xml.EndElement:
			if len(stack) == 0 {
				return webImageInvalid()
			}
			current := stack[len(stack)-1]
			if current.name == "style" {
				found, ok := webImageSVGStyles(current.style)
				if !ok || len(refs)+len(found) > 4096 {
					return webImageInvalid()
				}
				refs = append(refs, found...)
				if current.owner != "" {
					edges[current.owner] = append(edges[current.owner], found...)
				}
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				rootClosed = true
			}
		case xml.CharData:
			textBytes += len(v)
			if textBytes > 1<<20 {
				return webImageInvalid()
			}
			if len(stack) == 0 {
				if strings.TrimSpace(string(v)) != "" {
					return webImageInvalid()
				}
				continue
			}
			current := stack[len(stack)-1]
			if current.name == "style" {
				styleBytes += len(v)
				if styleBytes > 256<<10 {
					return webImageInvalid()
				}
				stack[len(stack)-1].style += string(v)
			} else if current.name != "title" && current.name != "desc" && current.name != "text" && current.name != "tspan" && strings.TrimSpace(string(v)) != "" {
				return webImageInvalid()
			}
		}
	}
	if !rootClosed || !rootSeen || len(stack) != 0 {
		return webImageInvalid()
	}
	for _, ref := range refs {
		if !ids[ref] {
			return webImageInvalid()
		}
	}
	state := map[string]uint8{}
	weight := map[string]int{}
	height := map[string]int{}
	var visit func(string, int) bool
	visit = func(id string, depth int) bool {
		if depth > 64 {
			return false
		}
		if state[id] != 0 {
			return state[id] == 2
		}
		state[id] = 1
		weight[id] = 1
		for _, next := range edges[id] {
			if !visit(next, depth+1) {
				return false
			}
			weight[id] += weight[next]
			if height[next]+1 > height[id] {
				height[id] = height[next] + 1
			}
			if weight[id] > 65536 || height[id] > 64 {
				return false
			}
		}
		state[id] = 2
		return true
	}
	for id := range ids {
		if !visit(id, 0) {
			return webImageInvalid()
		}
	}
	expanded := nodes
	for _, ref := range refs {
		expanded += weight[ref]
		if expanded > 65536 {
			return webImageInvalid()
		}
	}
	return nil
}
