package main

import (
	"compress/zlib"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"io"
	"log"
	"os"
	"path"

	"github.com/kettek/apng"
	"github.com/nfnt/resize"
)

var MaxDim uint

func main() {
	var inDir string
	var outDir string

	flag.StringVar(&inDir, "in", "", "Directory containing .gif files to be converted to square animated .pngs")
	flag.StringVar(&outDir, "out", "", "Directory containing where square .png files will be placed")
	flag.UintVar(&MaxDim, "max", 1024, "Max dimension (on either side). If larger than this, the animated .png will be resized to this dimension")
	flag.Parse()

	if inDir == "" || outDir == "" {
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Get list of inDir files ending in .gif
	gifFiles, err := getGifFiles(inDir)
	if err != nil {
		log.Fatalln("Error reading directory:", err)
	}

	// Run handleOne for every filename in the inDir list
	for _, fileName := range gifFiles {
		err := handleOne(inDir, fileName, outDir)
		if err != nil {
			log.Fatalf("Error handling file %s: %v\n", fileName, err)
		}
	}
}

func getGifFiles(dir string) ([]string, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var gifFiles []string
	for _, file := range files {
		if !file.IsDir() && path.Ext(file.Name()) == ".gif" {
			gifFiles = append(gifFiles, file.Name())
		}
	}

	return gifFiles, nil
}

// Via github.com/kettek/apngr
func handleOne(inDir, fileName, outDir string) error {

	gifFile, err := os.Open(path.Join(inDir, fileName))
	if err != nil {
		return err
	}
	g, err := gif.DecodeAll(gifFile)
	if err != nil {
		return err
	}

	a := apng.APNG{
		Frames: make([]apng.Frame, 1),
	}

	// Find if we need to repalettize.
	maxPaletteLength := -1
	mustRepalettize := false
	for frameIdx := 0; frameIdx < len(g.Image); frameIdx++ {
		if maxPaletteLength == -1 {
			maxPaletteLength = len(g.Image[frameIdx].Palette)
		}
		if len(g.Image[frameIdx].Palette) != maxPaletteLength {
			mustRepalettize = true
			break
		}
		for j := 0; j < len(g.Image); j++ {
			if j == frameIdx {
				continue
			}
			for k := 0; k < len(g.Image[frameIdx].Palette); k++ {
				r1, g1, b1, a1 := g.Image[frameIdx].Palette[k].RGBA()
				r2, g2, b2, a2 := g.Image[j].Palette[k].RGBA()
				if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
					mustRepalettize = true
					break
				}
			}
			if mustRepalettize {
				break
			}
		}
		if mustRepalettize {
			break
		}
	}

	// Analyze for RGBA upgrade
	uniqueColors := make(map[[4]uint32]struct{})
	for _, img := range g.Image {
		for _, p := range img.Palette {
			r, g, b, a := p.RGBA()
			k := [4]uint32{r, g, b, a}
			if _, ok := uniqueColors[k]; !ok {
				uniqueColors[k] = struct{}{}
			}
			if len(uniqueColors) > 256 {
				break
			}
		}
		if len(uniqueColors) > 256 {
			break
		}
	}

	var frames []image.Image

	if len(uniqueColors) > 256 {
		// Convert frames to APNG
		for _, frame := range g.Image {
			img := image.NewRGBA(frame.Bounds())
			draw.Draw(img, frame.Bounds(), frame, frame.Rect.Min, draw.Src)
			frames = append(frames, img)
		}
	} else if mustRepalettize {
		// Repalettize if we have less than 256 colors.
		insertColorEntry := func(f *image.Paletted, c color.Color) (int, error) {
			r1, g1, b1, a1 := c.RGBA()
			// Use existing entry if it exists.
			for entryIndex, entry := range f.Palette {
				r2, g2, b2, a2 := entry.RGBA()
				if r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2 {
					return entryIndex, nil
				}
			}
			// No such color exists, attempt to insert.
			if len(f.Palette) >= 256 {
				return -1, fmt.Errorf("attempted to produce a palette with >= 256 colors")
			}
			f.Palette = append(f.Palette, c)
			return len(f.Palette) - 1, nil
		}
		getClosestColorEntry := func(f *image.Paletted, c color.Color) int {
			return f.Palette.Index(c)
		}

		primaryFrame := g.Image[0]
		frames = append(frames, primaryFrame)

		for _, frame := range g.Image {
			if frame == primaryFrame {
				continue
			}
			m := make(map[int]int)
			for i := 0; i < len(frame.Palette); i++ {
				newIndex, err := insertColorEntry(primaryFrame, frame.Palette[i])
				if err == nil {
					m[i] = newIndex
				} else {
					newIndex = getClosestColorEntry(primaryFrame, frame.Palette[i])
					m[i] = newIndex
				}
			}
			// Make new pix referencing frame's pix to remap palette indices.
			p := make([]uint8, len(frame.Pix))
			for x := frame.Bounds().Min.X; x < frame.Bounds().Max.X; x++ {
				for y := frame.Bounds().Min.Y; y < frame.Bounds().Max.Y; y++ {
					i := frame.PixOffset(x, y)
					p[i] = uint8(m[int(frame.Pix[i])])
				}
			}
			frame.Pix = p
			frame.Palette = primaryFrame.Palette
			frames = append(frames, frame)
		}
	} else {
		// Otherwise write out as it is.
		for _, frame := range g.Image {
			frames = append(frames, frame)
		}
	}

	a.LoopCount = uint(g.LoopCount)
	a.Frames = make([]apng.Frame, len(frames))
	for i := 0; i < len(frames); i = i + 1 {
		currentFrameImg := frames[i]

		// Must be square for Signal stickers
		if x, y := currentFrameImg.Bounds().Dx(), currentFrameImg.Bounds().Dy(); x != y {
			if x > y {
				currentFrameImg = makeSquare(currentFrameImg, x, x, color.RGBA{0, 0, 0, 0})
			} else {
				currentFrameImg = makeSquare(currentFrameImg, y, y, color.RGBA{0, 0, 0, 0})
			}
		}

		// Limit the size
		if currentFrameImg.Bounds().Dx() > int(MaxDim) {
			currentFrameImg = resize.Resize(MaxDim, MaxDim, currentFrameImg, resize.Lanczos3)
		}

		a.Frames[i].Image = currentFrameImg
		a.Frames[i].XOffset = currentFrameImg.Bounds().Min.X
		a.Frames[i].YOffset = currentFrameImg.Bounds().Min.Y
		a.Frames[i].DelayNumerator = uint16(g.Delay[i])
		switch g.Disposal[i] {
		case gif.DisposalNone:
			a.Frames[i].DisposeOp = apng.DISPOSE_OP_NONE
		case gif.DisposalBackground:
			a.Frames[i].DisposeOp = apng.DISPOSE_OP_BACKGROUND
		case gif.DisposalPrevious:
			a.Frames[i].DisposeOp = apng.DISPOSE_OP_PREVIOUS
		}
		a.Frames[i].BlendOp = apng.BLEND_OP_OVER
	}

	outf, err := os.Create(path.Join(outDir, fileName+".png"))
	if err != nil {
		panic(err)
	}
	defer outf.Close()

	enc := apng.Encoder{
		CompressionWriter: func(w io.Writer) (apng.CompressionWriter, error) {
			return zlib.NewWriterLevel(w, 9)
		},
	}
	return enc.Encode(outf, a)
}

func makeSquare(img image.Image, newWidth, newHeight int, fillColor color.Color) image.Image {
	// Create a new RGBA image with the square dimensions
	squareImg := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))

	// Fill the new square image with the fill color
	for y := 0; y < newHeight; y++ {
		for x := 0; x < newWidth; x++ {
			squareImg.Set(x, y, fillColor)
		}
	}

	// Draw the original image onto the new square image
	draw.Draw(squareImg, img.Bounds(), img, image.Point{0, 0}, draw.Over)

	return squareImg
}
