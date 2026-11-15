package mapper

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/image/font/basicfont"
)

func TestMapHidesOnlyBasicMaterialsAndPreservesTotals(t *testing.T) {
	drops := []Drop{
		{SiteID: 5, ResourceID: 1, Quantity: 86},
		{SiteID: 5, ResourceID: 6, Quantity: 50},
		{SiteID: 5, ResourceID: 3, Quantity: 2},
		{SiteID: 5, ResourceType: "mysekai_item", ResourceID: 1},
		{SiteID: 5, ResourceType: "mysekai_fixture", ResourceID: 6},
	}
	before := Summarize(drops)
	groups := groupDropsByPoint(drops)
	labels, err := layoutSiteLabels(groups, 5, basicfont.Face7x13)
	if err != nil {
		t.Fatal(err)
	}
	want := []resourceKey{{Type: mysekaiMaterialType, ID: 3}, {Type: "mysekai_fixture", ID: 6}, {Type: "mysekai_item", ID: 1}}
	if len(labels) != 1 || !reflect.DeepEqual(labels[0].keys, want) {
		t.Fatalf("unexpected visible rewards: %+v", labels)
	}
	if groups[0].resources[resourceKey{Type: mysekaiMaterialType, ID: 1}] != 86 || groups[0].resources[resourceKey{Type: mysekaiMaterialType, ID: 6}] != 50 {
		t.Fatal("map filtering altered basic material quantities")
	}
	if after := Summarize(drops); !reflect.DeepEqual(after, before) {
		t.Fatalf("map filtering changed summary: %+v -> %+v", before, after)
	}
	labels, err = layoutSiteLabels(groupDropsByPoint(drops[:2]), 5, basicfont.Face7x13)
	if err != nil || len(labels) != 0 {
		t.Fatalf("basic-only map should have no labels: %+v, %v", labels, err)
	}
}

func TestDenseLabelsProtectIconsCountsAndAnchors(t *testing.T) {
	// Distant basic-only fixtures keep the map extent large while visible
	// rewards form a tight cluster, as in the real flower-garden archive.
	drops := []Drop{
		{SiteID: 7, ResourceID: 1, PositionX: -30, PositionZ: -30},
		{SiteID: 7, ResourceID: 1, PositionX: 30, PositionZ: 30},
	}
	for x := 0; x < 7; x++ {
		for z := 0; z < 7; z++ {
			drops = append(drops,
				Drop{SiteID: 7, ResourceID: 1, PositionX: float64(x), PositionZ: float64(z), Quantity: 6},
				Drop{SiteID: 7, ResourceID: 3, PositionX: float64(x), PositionZ: float64(z), Quantity: 12345},
				Drop{SiteID: 7, ResourceType: "mysekai_item", ResourceID: 7, PositionX: float64(x), PositionZ: float64(z)},
			)
		}
	}
	groups := groupDropsByPoint(drops)
	labels, err := layoutSiteLabels(groups, 7, basicfont.Face7x13)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 49 {
		t.Fatalf("lost labels: got %d, want 49", len(labels))
	}
	bounds := image.Rect(plotLeft+8, plotTop+8, plotRight-8, plotBottom-8)
	for i, label := range labels {
		if !label.rect.In(bounds) {
			t.Fatalf("label %d outside plot: %v", i, label.rect)
		}
		if label.rect.Dx() <= iconSize || label.rect.Dy() != 2*iconSize {
			t.Fatalf("label %d does not reserve counts and both rewards: %v", i, label.rect)
		}
		for j := 0; j < i; j++ {
			if label.rect.Overlaps(labels[j].rect) {
				t.Fatalf("labels %d and %d overlap: %v / %v", i, j, label.rect, labels[j].rect)
			}
		}
		for j, other := range labels {
			if i != j && other.anchor.In(label.rect) {
				t.Fatalf("label %d hides anchor %d", i, j)
			}
		}
	}
	again, err := layoutSiteLabels(groups, 7, basicfont.Face7x13)
	if err != nil || !reflect.DeepEqual(labels, again) {
		t.Fatalf("layout is not deterministic: %v", err)
	}
}

func TestMapLayoutReportsInsufficientSpaceInsteadOfHidingRewards(t *testing.T) {
	labels := []mapLabel{
		{anchor: image.Pt(5, 5), rect: image.Rect(0, 0, 42, 42)},
		{anchor: image.Pt(6, 6), rect: image.Rect(0, 0, 42, 42)},
	}
	if err := placeMapLabels(labels, image.Rect(0, 0, 50, 50)); err == nil {
		t.Fatal("expected explicit layout failure when two rewards cannot fit")
	}
}

// Regression cases from adjacent harvest fixtures: light wood was completely
// covered by petals, and one copper reward was covered by the next rock stack.
// Solid icons let us assert that every pixel survives in the final PNG, rather
// than merely asserting that all rewards reached the layout function.
func TestRenderedAdjacentRewardsRemainFullyVisible(t *testing.T) {
	red := color.RGBA{230, 30, 40, 255}
	blue := color.RGBA{20, 60, 220, 255}
	green := color.RGBA{10, 220, 30, 255}
	solid := func(c color.RGBA) image.Image {
		im := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
		draw.Draw(im, im.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
		return im
	}
	resources := map[int]Resource{
		1:  {Image: solid(green)},
		3:  {Image: solid(red)},
		6:  {Image: solid(green)},
		7:  {Image: solid(red)},
		23: {Image: solid(blue)},
	}
	cases := []struct {
		name                string
		site                int
		drops               []Drop
		redIcons, blueIcons int
	}{
		{"flower_garden", 7, []Drop{
			{ResourceID: 1, PositionX: -15, PositionZ: -26},
			{ResourceID: 1, PositionX: 20, PositionZ: 18},
			{ResourceID: 1, PositionX: -2, PositionZ: -25, Quantity: 6},
			{ResourceID: 3, PositionX: -2, PositionZ: -25, Quantity: 2},
			{ResourceID: 23, PositionX: -2, PositionZ: -24},
			{ResourceID: 23, PositionX: -2, PositionZ: -23},
		}, 1, 2},
		{"memorial_place", 8, []Drop{
			{ResourceID: 6, PositionX: -30, PositionZ: -27},
			{ResourceID: 6, PositionX: 15, PositionZ: 14},
			{ResourceID: 6, PositionX: 10, PositionZ: -27, Quantity: 6},
			{ResourceID: 7, PositionX: 10, PositionZ: -27},
			{ResourceType: "mysekai_item", ResourceID: 7, PositionX: 10, PositionZ: -27},
			{ResourceID: 6, PositionX: 11, PositionZ: -27, Quantity: 6},
			{ResourceID: 7, PositionX: 11, PositionZ: -27, Quantity: 2},
			{ResourceType: "mysekai_item", ResourceID: 7, PositionX: 11, PositionZ: -27},
		}, 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "map.png")
			if err := renderSite(tc.drops, tc.site, resources, basicfont.Face7x13, basicfont.Face7x13, path); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			im, err := png.Decode(file)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[color.RGBA]int{}
			for y := 0; y < canvasHeight; y++ {
				for x := 0; x < canvasWidth; x++ {
					counts[color.RGBAModel.Convert(im.At(x, y)).(color.RGBA)]++
				}
			}
			if counts[red] != tc.redIcons*iconSize*iconSize || counts[blue] != tc.blueIcons*iconSize*iconSize {
				t.Fatalf("reward icons were lost or covered: red=%d, blue=%d", counts[red], counts[blue])
			}
			if counts[green] != 0 {
				t.Fatal("basic material icons should not appear on the map")
			}
			badgeBackground := color.RGBA{225, 238, 255, 255}
			badge := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
			drawResourceBadge(badge, resourceKey{Type: "mysekai_item", ID: 7}, 0, 0)
			badgePixels := 0
			for y := 0; y < iconSize; y++ {
				for x := 0; x < iconSize; x++ {
					if badge.RGBAAt(x, y) == badgeBackground {
						badgePixels++
					}
				}
			}
			badgeCount := 0
			for _, drop := range tc.drops {
				if drop.ResourceType == "mysekai_item" {
					badgeCount++
				}
			}
			if counts[badgeBackground] != badgeCount*badgePixels {
				t.Fatalf("item badges were lost or covered: got %d background pixels, want %d", counts[badgeBackground], badgeCount*badgePixels)
			}
		})
	}
}
