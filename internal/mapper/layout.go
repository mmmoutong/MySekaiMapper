package mapper

import (
	"fmt"
	"image"
	"math"
	"slices"
	"sort"

	"golang.org/x/image/font"
)

const labelGap = 6

type mapLabel struct {
	group  pointGroup
	keys   []resourceKey
	anchor image.Point
	rect   image.Rectangle // Includes icons and quantity text.
}

// Filtering is confined to presentation: parsing, grouping and summaries keep
// all rewards. IDs from other resource types must not be hidden as materials.
func visibleResourceKeys(group pointGroup) []resourceKey {
	keys := make([]resourceKey, 0, len(group.resources))
	for key := range group.resources {
		if _, hidden := hiddenMapMaterialIDs[key.ID]; hidden && key.Type == mysekaiMaterialType {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Type == keys[j].Type {
			return keys[i].ID < keys[j].ID
		}
		if keys[i].Type == mysekaiMaterialType {
			return true
		}
		if keys[j].Type == mysekaiMaterialType {
			return false
		}
		return keys[i].Type < keys[j].Type
	})
	return keys
}

func layoutSiteLabels(groups []pointGroup, siteID int, countFace font.Face) ([]mapLabel, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	minX, maxX := math.Inf(1), math.Inf(-1)
	minZ, maxZ := math.Inf(1), math.Inf(-1)
	maxStack := 1
	for _, group := range groups {
		x, z := Rotate(group.x, group.z, siteID)
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minZ, maxZ = math.Min(minZ, z), math.Max(maxZ, z)
		maxStack = max(maxStack, len(visibleResourceKeys(group)))
	}
	minX, maxX = minX-2, maxX+2
	minZ, maxZ = minZ-2, maxZ+2
	left, right := plotLeft+iconSize/2, plotRight-iconSize/2-90
	top, bottom := plotTop+iconSize/2, plotBottom-iconSize/2-(maxStack-1)*iconStep
	labels := make([]mapLabel, 0, len(groups))
	for _, group := range groups {
		keys := visibleResourceKeys(group)
		if len(keys) == 0 {
			continue
		}
		x, z := Rotate(group.x, group.z, siteID)
		anchor := image.Pt(
			left+int(math.Round((x-minX)/(maxX-minX)*float64(right-left))),
			top+int(math.Round((maxZ-z)/(maxZ-minZ)*float64(bottom-top))),
		)
		width := iconSize
		for _, key := range keys {
			if count := group.resources[key]; count > 1 {
				text := fmt.Sprintf("×%d", count)
				ink, advance := font.BoundString(countFace, text)
				width = max(width, iconSize+5+max(ink.Max.X.Ceil(), advance.Ceil())+2)
			}
		}
		rect := image.Rect(anchor.X-iconSize/2, anchor.Y-iconSize/2, anchor.X-iconSize/2+width, anchor.Y-iconSize/2+len(keys)*iconStep)
		labels = append(labels, mapLabel{group: group, keys: keys, anchor: anchor, rect: rect})
	}
	return labels, placeMapLabels(labels, image.Rect(plotLeft+8, plotTop+8, plotRight-8, plotBottom-8))
}

// Keep an unobstructed label at its original position. Otherwise find the
// closest free rectangle, reserving both previous labels and map anchors.
// Obstacle edges contain the nearest feasible position for axis-aligned boxes;
// searching their intersections avoids a pixel-by-pixel scan or random jitter.
func placeMapLabels(labels []mapLabel, bounds image.Rectangle) error {
	anchors := make([]image.Rectangle, len(labels))
	for i, label := range labels {
		anchors[i] = image.Rect(label.anchor.X-3, label.anchor.Y-3, label.anchor.X+4, label.anchor.Y+4)
	}
	placed := make([]image.Rectangle, 0, len(labels))
	for i := range labels {
		preferred := labels[i].rect
		obstacles := append([]image.Rectangle(nil), placed...)
		for j, anchor := range anchors {
			if i != j {
				obstacles = append(obstacles, anchor)
			}
		}
		if freeLabelRect(preferred, bounds, obstacles) {
			placed = append(placed, preferred.Inset(-labelGap))
			continue
		}
		// A moved label must also leave its own coordinate marker visible.
		obstacles = append(obstacles, anchors[i])
		width, height := preferred.Dx(), preferred.Dy()
		if width > bounds.Dx() || height > bounds.Dy() {
			return fmt.Errorf("drop label at (%g, %g) exceeds plot bounds", labels[i].group.x, labels[i].group.z)
		}
		xs := []int{preferred.Min.X, bounds.Min.X, bounds.Max.X - width}
		ys := []int{preferred.Min.Y, bounds.Min.Y, bounds.Max.Y - height}
		for _, obstacle := range obstacles {
			xs = append(xs, obstacle.Min.X-width, obstacle.Max.X)
			ys = append(ys, obstacle.Min.Y-height, obstacle.Max.Y)
		}
		slices.Sort(xs)
		slices.Sort(ys)
		xs, ys = slices.Compact(xs), slices.Compact(ys)
		bestDistance := int64(math.MaxInt64)
		var best image.Rectangle
		for _, y := range ys {
			for _, x := range xs {
				dx, dy := int64(x-preferred.Min.X), int64(y-preferred.Min.Y)
				distance := dx*dx + dy*dy
				if distance >= bestDistance {
					continue
				}
				candidate := image.Rect(x, y, x+width, y+height)
				if freeLabelRect(candidate, bounds, obstacles) {
					best, bestDistance = candidate, distance
				}
			}
		}
		if bestDistance == math.MaxInt64 {
			return fmt.Errorf("no room for drop label at (%g, %g)", labels[i].group.x, labels[i].group.z)
		}
		labels[i].rect = best
		placed = append(placed, best.Inset(-labelGap))
	}
	return nil
}

func freeLabelRect(rect, bounds image.Rectangle, obstacles []image.Rectangle) bool {
	if !rect.In(bounds) {
		return false
	}
	for _, obstacle := range obstacles {
		if rect.Overlaps(obstacle) {
			return false
		}
	}
	return true
}
