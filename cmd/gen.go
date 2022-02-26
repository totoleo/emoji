/*
Copyright © 2022 NAME HERE <EMAIL ADDRESS>

*/
package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/ioutil"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/bytedance/gopkg/lang/fastrand"
	"golang.org/x/image/draw"

	"github.com/dhconnelly/rtreego"
	"github.com/spf13/cobra"
)

var input string
var out string
var emojis string
var pixels int
var scale float64
var frames int

const (
	// Where emoji images can be found. Relative to bin.
	EmojiPath = "emojis"
	// Input size of emoji images.
	EmojiSize = 72
	// How many nearest emojis to pick from randomly.
	EmojiJitter = 3
)

// genCmd represents the gen command
var genCmd = &cobra.Command{
	Use:   "gen",
	Short: "generate a mosaic gif with emoji",
	Long:  printUsage(),
	Run: func(cmd *cobra.Command, args []string) {
		if pixels <= 0 || pixels > EmojiSize {
			printUsage()
			return
		}
		if scale <= 0 || frames <= 0 {
			printUsage()
			return
		}
		emoji(input, out, emojis, pixels, scale, frames)
	},
}

func init() {
	rootCmd.AddCommand(genCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// genCmd.PersistentFlags().String("foo", "", "A help for foo")

	genCmd.Flags().StringVarP(&input, "input", "i", "", "input file")
	genCmd.Flags().StringVarP(&out, "out", "o", "", "output file")
	genCmd.Flags().StringVarP(&emojis, "emojis", "e", "emojis", "emojis for generation")
	genCmd.Flags().IntVarP(&pixels, "pixels", "p", 1, "emoji size")
	genCmd.Flags().Float64VarP(&scale, "scale", "s", 1, "scale for output")
	genCmd.Flags().IntVarP(&frames, "frames", "f", 1, "frames")

	image.RegisterFormat("png", "png", png.Decode, png.DecodeConfig)
	image.RegisterFormat("jpg", "jpg", jpeg.Decode, jpeg.DecodeConfig)

	log.SetFlags(log.Lshortfile)
}

type emojix struct {
	source map[string]image.Image
	tree   *rtreego.Rtree
}

func newEmoji(path string, pixelSize int) (*emojix, error) {
	emojiImgs, err := emojiImages(path, pixelSize)
	if err != nil {
		return nil, err
	}
	if len(emojiImgs) == 0 {
		return nil, errors.New("emojis folder is empty")
	}

	emojiClrs := emojiColors(emojiImgs)

	tree := createSearchTree(emojiClrs)
	e := &emojix{}
	e.source = emojiImgs
	e.tree = tree
	return e, nil
}
func emoji(inImgPath, out, EmojiPath string, pixelSize int, scale float64, frames int) {

	fmt.Printf("Loading emoji images for size: %d...\n", pixelSize)
	emoj, err := newEmoji(EmojiPath, pixelSize)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Loading input image...")
	img, err := loadImage(inImgPath)
	if err != nil {
		log.Fatal(err)
	}
	imgBounds := img.Bounds()

	scaledPX := int(math.Round(float64(imgBounds.Min.X) * scale))
	scaledPY := int(math.Round(float64(imgBounds.Min.Y) * scale))
	scaledQX := int(math.Round(float64(imgBounds.Max.X) * scale))
	scaledQY := int(math.Round(float64(imgBounds.Max.Y) * scale))
	scaledImgBounds := image.Rect(scaledPX, scaledPY, scaledQX, scaledQY)

	similarity := emoj.learn(pixelSize, scale, img)

	outImgs := make([]*image.Paletted, frames)
	delays := make([]int, frames)
	wg := sync.WaitGroup{}
	wg.Add(frames)
	for i := 0; i < frames; i++ {
		outImgs[i] = image.NewPaletted(scaledImgBounds, palette.WebSafe)
		delays[i] = 20
		index := i
		fmt.Printf("Writing output image frame: %d...\n", i+1)
		go func() {
			defer wg.Done()
			for y := scaledImgBounds.Min.Y; y < scaledImgBounds.Max.Y+pixelSize; y += pixelSize {
				for x := scaledImgBounds.Min.X; x < scaledImgBounds.Max.X+pixelSize; x += pixelSize {
					samplePX := int(math.Round(float64(x) / scale))
					samplePY := int(math.Round(float64(y) / scale))

					emojiImg := similarity.Get([2]int{samplePX, samplePY})

					dstPX := x
					dstPY := y
					dstQX := x + pixelSize
					dstQY := y + pixelSize
					dstRegion := image.Rect(dstPX, dstPY, dstQX, dstQY)
					draw.Draw(outImgs[index], dstRegion, emojiImg, image.Point{}, draw.Src)
				}
			}
		}()
	}
	wg.Wait()
	fmt.Println("Generating image...")
	f, err := os.Create(out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	_ = gif.EncodeAll(f, &gif.GIF{
		Image: outImgs,
		Delay: delays,
	})

	fmt.Println("Done.")
}

type similarity struct {
	e          *emojix
	similarity map[[2]int][]rtreego.Spatial
}

func (s *similarity) Get(key [2]int) image.Image {
	items, ok := s.similarity[key]
	if !ok {
		return nil
	}
	idx := fastrand.Intn(len(items))
	item, ok := items[idx].(*EmojiItem)
	if !ok {
		return nil
	}
	name := item.Name()
	emojiImg := s.e.source[name]
	return emojiImg
}

func (e *emojix) learn(pixelSize int, scale float64, img image.Image) *similarity {
	imgBounds := img.Bounds()

	scaledPX := int(math.Round(float64(imgBounds.Min.X) * scale))
	scaledPY := int(math.Round(float64(imgBounds.Min.Y) * scale))
	scaledQX := int(math.Round(float64(imgBounds.Max.X) * scale))
	scaledQY := int(math.Round(float64(imgBounds.Max.Y) * scale))
	bounds := image.Rect(scaledPX, scaledPY, scaledQX, scaledQY)

	var s = map[[2]int][]rtreego.Spatial{}
	for y := bounds.Min.Y; y < bounds.Max.Y+pixelSize; y += pixelSize {
		for x := bounds.Min.X; x < bounds.Max.X+pixelSize; x += pixelSize {
			samplePX := int(math.Round(float64(x) / scale))
			samplePY := int(math.Round(float64(y) / scale))
			sampleQX := int(math.Round(float64(x+pixelSize) / scale))
			sampleQY := int(math.Round(float64(y+pixelSize) / scale))
			sampleRegion := image.Rect(samplePX, samplePY, sampleQX, sampleQY)

			pixels := imagePixels(img, sampleRegion)
			clr := averageColor(pixels)

			if clr.A == 0 {
				continue
			}

			items := e.tree.NearestNeighbors(EmojiJitter, colorToPoint(clr))
			s[[2]int{samplePX, samplePY}] = items
		}
	}
	return &similarity{
		e:          e,
		similarity: s,
	}
}

type EmojiItem struct {
	name string
	rect *rtreego.Rect
}

func NewEmojiItem(name string, avgClr color.RGBA) *EmojiItem {
	point := colorToPoint(avgClr)
	rect, err := rtreego.NewRectFromPoints(point, point)
	if err != nil {
		log.Fatal(err)
	}

	return &EmojiItem{
		name: name,
		rect: rect,
	}
}

func (e *EmojiItem) Name() string {
	return e.name
}

func (e *EmojiItem) Bounds() *rtreego.Rect {
	return e.rect
}

func printUsage() string {
	return "Usage: moji <input_image.(png|jpg)> <output_image.gif> <pixel_size [1, 72]> <scale (0,]> <frames (0,]>"
}

func loadImage(path string) (image.Image, error) {
	file, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	img, _, err := image.Decode(bytes.NewReader(file))
	if err != nil {
		return nil, err
	}

	return img, nil
}

func createSearchTree(emojiClrs map[string]color.RGBA) *rtreego.Rtree {
	rt := rtreego.NewTree(4, 25, 200)
	for name, clr := range emojiClrs {
		item := NewEmojiItem(name, clr)
		rt.Insert(item)
	}

	return rt
}

func findNearestEmoji(tree *rtreego.Rtree, emojiImgs map[string]image.Image, clr color.RGBA) (image.Image, error) {
	items := tree.NearestNeighbors(EmojiJitter, colorToPoint(clr))
	idx := fastrand.Intn(len(items))
	item, ok := items[idx].(*EmojiItem)
	if !ok {
		return nil, errors.New("tree item couldn't be cast to an EmojiItem")
	}

	name := item.Name()
	img := emojiImgs[name]

	return img, nil
}

func emojiImages(assetPath string, size int) (map[string]image.Image, error) {

	files, err := ioutil.ReadDir(assetPath)
	if err != nil {
		return nil, err
	}
	res := make(map[string]image.Image, len(files))

	//filter := rez.NewBicubicFilter()
	for _, file := range files {

		name := file.Name()
		if file.IsDir() {
			continue
		}
		srcImg, err := loadImage(filepath.Join(assetPath, file.Name()))
		if err != nil {
			log.Printf("ignore file:%s with reason:%v", file.Name(), err)
			continue
		}
		img := image.NewRGBA(image.Rect(0, 0, size, size))
		//err = rez.Convert(img, srcImg, filter)
		//if err != nil {
		//	return nil, fmt.Errorf("resize err:%w", err)
		//}
		draw.NearestNeighbor.Scale(img, img.Bounds(), srcImg, srcImg.Bounds(), draw.Src, nil)
		res[name] = img
	}

	return res, nil
}

func emojiColors(emojiImgs map[string]image.Image) map[string]color.RGBA {
	res := make(map[string]color.RGBA, len(emojiImgs))

	for name, img := range emojiImgs {
		pxls := imagePixels(img, img.Bounds())
		avgClr := averageColor(pxls)

		res[name] = avgClr
	}

	return res
}

func imagePixels(img image.Image, region image.Rectangle) []color.RGBA {
	pX := region.Min.X
	pY := region.Min.Y
	qX := region.Max.X
	qY := region.Max.Y

	// If the provided region is outside of the image bounds, clamp it to
	// the nearest complete region within the image.
	imgBounds := img.Bounds()
	imgWidth := imgBounds.Max.X
	imgHeight := imgBounds.Max.Y
	if imgWidth < qX {
		pX = imgWidth - region.Dx()
	}
	if imgHeight < qY {
		pY = imgHeight - region.Dy()
	}

	var pixels = make([]color.RGBA, 0, (qX-pX+1)*(qY-pY+1))
	for y := pY; y < qY; y++ {
		for x := pX; x < qX; x++ {
			pixel := img.At(x, y)

			r, g, b, a := rgba32ToRGBA8(pixel.RGBA())
			rgba := color.RGBA{R: r, G: g, B: b, A: a}

			pixels = append(pixels, rgba)
		}
	}

	return pixels
}

func rgba32ToRGBA8(r32, g32, b32, a32 uint32) (uint8, uint8, uint8, uint8) {
	r := uint8(r32 >> 8)
	g := uint8(g32 >> 8)
	b := uint8(b32 >> 8)
	a := uint8(a32 >> 8)

	return r, g, b, a
}

func averageColor(clrs []color.RGBA) color.RGBA {
	rComp := uint64(0)
	gComp := uint64(0)
	bComp := uint64(0)
	aComp := uint64(0)
	pixelsCount := uint64(len(clrs))

	if pixelsCount == 0 {
		return color.RGBA{R: 0, G: 0, B: 0, A: 0}
	}

	for _, clr := range clrs {
		r, g, b, a := clr.RGBA()

		r64 := uint64(r >> 8)
		g64 := uint64(g >> 8)
		b64 := uint64(b >> 8)
		a64 := uint64(a >> 8)

		rComp += r64 * r64
		gComp += g64 * g64
		bComp += b64 * b64
		aComp += a64 * a64
	}

	rAvg := rComp / pixelsCount
	gAvg := gComp / pixelsCount
	bAvg := bComp / pixelsCount
	aAvg := aComp / pixelsCount

	r := uint8(math.Sqrt(float64(rAvg)))
	g := uint8(math.Sqrt(float64(gAvg)))
	b := uint8(math.Sqrt(float64(bAvg)))
	a := uint8(math.Sqrt(float64(aAvg)))

	return color.RGBA{R: r, G: g, B: b, A: a}
}

func colorToPoint(clr color.RGBA) rtreego.Point {
	res := make([]float64, 4)

	r, g, b, a := rgba32ToRGBA8(clr.RGBA())

	rFloat := float64(r)
	gFloat := float64(g)
	bFloat := float64(b)
	aFloat := float64(a)

	res[0] = rFloat
	res[1] = gFloat
	res[2] = bFloat
	res[3] = aFloat

	return res
}
