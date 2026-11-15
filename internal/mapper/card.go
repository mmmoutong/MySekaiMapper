package mapper

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"

	"golang.org/x/image/font"
	"golang.org/x/image/vector"
)

var (
	mapInk   = color.RGBA{48, 64, 84, 255}
	mapMuted = color.RGBA{117, 132, 152, 255}
)

func drawMapCard(canvas *image.RGBA, siteID int, titleFace, captionFace font.Face) {
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{238, 243, 249, 255}), image.Point{}, draw.Src)
	fillRoundedRect(canvas, image.Rect(34, 38, canvasWidth-34, canvasHeight-26), 32, color.RGBA{225, 232, 242, 255})
	fillRoundedRect(canvas, image.Rect(32, 28, canvasWidth-32, canvasHeight-34), 32, color.White)

	accent := color.RGBA{99, 128, 153, 255}
	switch siteID {
	case 5:
		accent = color.RGBA{91, 139, 116, 255}
	case 6:
		accent = color.RGBA{83, 139, 173, 255}
	case 7:
		accent = color.RGBA{145, 116, 167, 255}
	}
	fillRoundedRect(canvas, image.Rect(plotLeft, 77, plotLeft+6, 119), 3, accent)
	name, ok := siteNames[siteID]
	if !ok {
		name = fmt.Sprintf("采集区域 %d", siteID)
	}
	drawText(canvas, titleFace, plotLeft+28, 119, name)
	badge := image.Rect(plotRight-140, 80, plotRight, 122)
	fillRoundedRect(canvas, badge, 14, color.RGBA{239, 244, 250, 255})
	text := fmt.Sprintf("区域 %02d", siteID)
	drawTextColor(canvas, captionFace, badge.Min.X+(badge.Dx()-font.MeasureString(captionFace, text).Ceil())/2, 108, text, accent)
	divider := color.RGBA{233, 238, 245, 255}
	draw.Draw(canvas, image.Rect(plotLeft, 186, plotRight, 187), image.NewUniform(divider), image.Point{}, draw.Src)
}

// Rasterize rounded corners with antialiasing at the output resolution.
func fillRoundedRect(dst *image.RGBA, rect image.Rectangle, radius float32, fill color.Color) {
	w, h := float32(rect.Dx()), float32(rect.Dy())
	r := min(radius, min(w, h)/2)
	const k = float32(0.55228475)
	path := vector.NewRasterizer(rect.Dx(), rect.Dy())
	path.MoveTo(r, 0)
	path.LineTo(w-r, 0)
	path.CubeTo(w-r+k*r, 0, w, r-k*r, w, r)
	path.LineTo(w, h-r)
	path.CubeTo(w, h-r+k*r, w-r+k*r, h, w-r, h)
	path.LineTo(r, h)
	path.CubeTo(r-k*r, h, 0, h-r+k*r, 0, h-r)
	path.LineTo(0, r)
	path.CubeTo(0, r-k*r, r-k*r, 0, r, 0)
	path.ClosePath()
	path.Draw(dst, rect, image.NewUniform(fill), image.Point{})
}
