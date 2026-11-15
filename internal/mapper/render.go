package mapper

import (
	"fmt"
	"image"
	"image/color"
	stdDraw "image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	canvasWidth  = 1800
	canvasHeight = 1800
	plotLeft     = 84
	plotRight    = canvasWidth - 84
	plotTop      = 206
	plotBottom   = 1628
	iconSize     = 42
	iconStep     = 42
)

var (
	hiddenMapMaterialIDs = map[int]struct{}{1: {}, 6: {}}
	rareIDs              = []int{5, 12, 20, 24}
	siteNames            = map[int]string{5: "初始空地", 6: "心愿沙滩", 7: "烂漫花田", 8: "忘却之所"}
)

// GenerateResult describes generated artifacts without retaining plaintext or
// raw archive data.
type GenerateResult struct {
	Summary  Summary
	MapFiles []string
	RareFile string
}

// Generate renders every site represented in drops and writes the established
// rare_resources.txt output format.
func Generate(drops []Drop, resourceCSV, fontPath, outputDir string) (GenerateResult, error) {
	if len(drops) == 0 {
		return GenerateResult{}, fmt.Errorf("no drops found in the save file")
	}
	resources, err := LoadResources(resourceCSV)
	if err != nil {
		return GenerateResult{}, err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return GenerateResult{}, fmt.Errorf("create output directory: %w", err)
	}

	face := loadFace(fontPath, 30)
	countFace := loadFace(fontPath, 14)
	bySite := map[int][]Drop{}
	for _, drop := range drops {
		bySite[drop.SiteID] = append(bySite[drop.SiteID], drop)
	}

	result := GenerateResult{Summary: Summarize(drops)}
	for _, siteID := range sortedSiteIDs(drops) {
		outPath := filepath.Join(outputDir, fmt.Sprintf("site_%d.png", siteID))
		if err := renderSite(bySite[siteID], siteID, resources, face, countFace, outPath); err != nil {
			return GenerateResult{}, err
		}
		result.MapFiles = append(result.MapFiles, outPath)
	}

	rarePath, err := WriteRareResources(drops, resources, outputDir)
	if err != nil {
		return GenerateResult{}, err
	}
	result.RareFile = rarePath
	return result, nil
}

func loadFace(fontPath string, size float64) font.Face {
	fontData, err := os.ReadFile(fontPath)
	if err != nil {
		return basicfont.Face7x13
	}
	parsed, err := opentype.Parse(fontData)
	if err != nil {
		return basicfont.Face7x13
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 96, Hinting: font.HintingFull})
	if err != nil {
		return basicfont.Face7x13
	}
	return face
}

type pointKey struct {
	x float64
	z float64
}

type pointGroup struct {
	x         float64
	z         float64
	resources map[resourceKey]int
}

func groupDropsByPoint(drops []Drop) []pointGroup {
	groupsByPoint := map[pointKey]map[resourceKey]int{}
	for _, drop := range drops {
		key := pointKey{x: drop.PositionX, z: drop.PositionZ}
		if groupsByPoint[key] == nil {
			groupsByPoint[key] = map[resourceKey]int{}
		}
		groupsByPoint[key][drop.resourceKey()] += drop.quantity()
	}
	groups := make([]pointGroup, 0, len(groupsByPoint))
	for key, groupedResources := range groupsByPoint {
		groups = append(groups, pointGroup{x: key.x, z: key.z, resources: groupedResources})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].x == groups[j].x {
			return groups[i].z < groups[j].z
		}
		return groups[i].x < groups[j].x
	})
	return groups
}

func renderSite(drops []Drop, siteID int, resources map[int]Resource, face, countFace font.Face, outPath string) error {
	groups := groupDropsByPoint(drops)
	if len(groups) == 0 {
		return fmt.Errorf("site %d has no drops", siteID)
	}

	labels, err := layoutSiteLabels(groups, siteID, countFace)
	if err != nil {
		return fmt.Errorf("layout site %d: %w", siteID, err)
	}

	canvas := image.NewRGBA(image.Rect(0, 0, canvasWidth, canvasHeight))
	drawMapCard(canvas, drops, groups, labels, siteID, resources, face, countFace)

	// Draw connections before any labels, so even crossing lines cannot erase
	// an icon or its quantity. Displaced labels retain their actual map anchor.
	for _, label := range labels {
		if !label.anchor.In(label.rect) {
			end := image.Pt(
				max(label.rect.Min.X, min(label.anchor.X, label.rect.Max.X-1)),
				max(label.rect.Min.Y, min(label.anchor.Y, label.rect.Max.Y-1)),
			)
			drawMapConnection(canvas, label.anchor, end)
			fillRoundedRect(canvas, image.Rect(label.anchor.X-3, label.anchor.Y-3, label.anchor.X+3, label.anchor.Y+3), 3, mapMuted)
		}
	}
	for _, label := range labels {
		for index, key := range label.keys {
			offset := index * iconStep
			x, y := label.rect.Min.X, label.rect.Min.Y+offset
			resource, ok := resources[key.ID]
			if key.Type == mysekaiMaterialType && ok && resource.Image != nil {
				pasteScaled(canvas, resource.Image, x, y, iconSize)
			} else {
				drawResourceBadge(canvas, key, x, y, countFace)
			}
			count := label.group.resources[key]
			if count > 1 {
				drawText(canvas, countFace, x+iconSize+5, y+28, fmt.Sprintf("×%d", count))
			}
		}
	}

	if err := writePNGAtomically(outPath, canvas); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}

func drawMapConnection(canvas *image.RGBA, from, to image.Point) {
	dx, dy := to.X-from.X, to.Y-from.Y
	steps := max(absInt(dx), absInt(dy))
	for i := 0; i <= steps; i++ {
		point := from
		if steps > 0 {
			point = image.Pt(from.X+dx*i/steps, from.Y+dy*i/steps)
		}
		canvas.Set(point.X, point.Y, color.RGBA{178, 190, 206, 255})
	}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// The CSV contains MySekai material icons only. A type-specific badge keeps
// items, furniture and records visible without borrowing a material's icon.
func drawResourceBadge(canvas *image.RGBA, key resourceKey, x, y int, faces ...font.Face) {
	label := "?"
	background := color.RGBA{235, 235, 235, 255}
	switch key.Type {
	case mysekaiMaterialType:
		label = "M"
	case "mysekai_item":
		label = "I"
		background = color.RGBA{225, 238, 255, 255}
	case "mysekai_fixture":
		label = "F"
		background = color.RGBA{255, 237, 212, 255}
	case "mysekai_music_record":
		label = "R"
		background = color.RGBA{238, 224, 255, 255}
	}
	rect := image.Rect(x, y, x+iconSize, y+iconSize)
	fillRoundedRect(canvas, rect, 8, background)
	id := fmt.Sprint(key.ID)
	drawTextColor(canvas, basicfont.Face7x13, x+(iconSize-7)/2, y+14, label, mapMuted)
	var idFace font.Face = basicfont.Face7x13
	if len(faces) > 0 && font.MeasureString(faces[0], id).Ceil() <= iconSize-8 {
		idFace = faces[0]
	}
	drawText(canvas, idFace, x+(iconSize-font.MeasureString(idFace, id).Ceil())/2, y+34, id)
}

func writePNGAtomically(path string, source image.Image) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	defer file.Close()
	if err := file.Chmod(0o644); err != nil {
		return err
	}

	if err := png.Encode(file, source); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func writeTextAtomically(path, content string) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	defer file.Close()
	if err := file.Chmod(0o644); err != nil {
		return err
	}

	if _, err := io.WriteString(file, content); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func pasteScaled(dst *image.RGBA, src image.Image, x, y, size int) {
	thumb := image.NewRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(thumb, thumb.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	stdDraw.Draw(dst, image.Rect(x, y, x+size, y+size), thumb, image.Point{}, stdDraw.Over)
}

func drawText(dst *image.RGBA, face font.Face, x, y int, value string) {
	drawTextColor(dst, face, x, y, value, mapInk)
}

func drawTextColor(dst *image.RGBA, face font.Face, x, y int, value string, ink color.Color) {
	drawer := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(ink),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	drawer.DrawString(value)
}

// WriteRareResources preserves the existing text format so notification code
// can consume it unchanged during compatibility tests.
func WriteRareResources(drops []Drop, resources map[int]Resource, outputDir string) (string, error) {
	lines := []string{"稀有资源统计", ""}
	for _, resourceID := range rareIDs {
		count := 0
		sites := map[int]struct{}{}
		for _, drop := range drops {
			if drop.ResourceID != resourceID || drop.resourceKey().Type != mysekaiMaterialType {
				continue
			}
			count += drop.quantity()
			sites[drop.SiteID] = struct{}{}
		}
		siteIDs := make([]int, 0, len(sites))
		for siteID := range sites {
			siteIDs = append(siteIDs, siteID)
		}
		sort.Ints(siteIDs)
		siteLabels := make([]string, 0, len(siteIDs))
		for _, siteID := range siteIDs {
			if name, ok := siteNames[siteID]; ok {
				siteLabels = append(siteLabels, name)
			} else {
				siteLabels = append(siteLabels, fmt.Sprintf("site%d", siteID))
			}
		}
		suffix := ""
		if len(siteLabels) > 0 {
			suffix = fmt.Sprintf("（%s）", strings.Join(siteLabels, "、"))
		}
		name := fmt.Sprint(resourceID)
		if resource, ok := resources[resourceID]; ok && resource.Name != "" {
			name = resource.Name
		}
		lines = append(lines, fmt.Sprintf("%s × %d %s", name, count, suffix))
	}

	path := filepath.Join(outputDir, "rare_resources.txt")
	if err := writeTextAtomically(path, strings.Join(lines, "\n")); err != nil {
		return "", fmt.Errorf("write rare resources: %w", err)
	}
	return path, nil
}
