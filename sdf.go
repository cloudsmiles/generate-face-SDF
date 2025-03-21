package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

type Point struct {
	dx, dy int
}

func (p Point) distSq() int {
	return p.dx*p.dx + p.dy*p.dy
}

type SDFGenerator struct {
	WIDTH  int
	HEIGHT int
	empty  Point
	inside Point
	grid1  [][]Point
	grid2  [][]Point
}

func NewSDFGenerator() *SDFGenerator {
	return &SDFGenerator{
		empty:  Point{9999, 9999},
		inside: Point{0, 0},
	}
}

func (g *SDFGenerator) getPoint(grid [][]Point, x, y int) Point {
	if x >= 0 && x < g.WIDTH && y >= 0 && y < g.HEIGHT {
		return grid[y][x]
	}
	return g.empty
}

func (g *SDFGenerator) compare(grid [][]Point, p Point, x, y, offsetX, offsetY int) Point {
	other := g.getPoint(grid, x+offsetX, y+offsetY)
	other.dx += offsetX
	other.dy += offsetY
	if other.distSq() < p.distSq() {
		return other
	}
	return p
}

func (g *SDFGenerator) generateSDF(grid [][]Point) [][]Point {
	// 第一次扫描
	for y := 0; y < g.HEIGHT; y++ {
		for x := 0; x < g.WIDTH; x++ {
			p := grid[y][x]
			for _, offset := range [][2]int{{-1, 0}, {0, -1}, {-1, -1}, {1, -1}} {
				p = g.compare(grid, p, x, y, offset[0], offset[1])
			}
			grid[y][x] = p
		}

		for x := g.WIDTH - 1; x >= 0; x-- {
			p := grid[y][x]
			p = g.compare(grid, p, x, y, 1, 0)
			grid[y][x] = p
		}
	}

	// 第二次扫描
	for y := g.HEIGHT - 1; y >= 0; y-- {
		for x := g.WIDTH - 1; x >= 0; x-- {
			p := grid[y][x]
			for _, offset := range [][2]int{{1, 0}, {0, 1}, {-1, 1}, {1, 1}} {
				p = g.compare(grid, p, x, y, offset[0], offset[1])
			}
			grid[y][x] = p
		}

		for x := 0; x < g.WIDTH; x++ {
			p := grid[y][x]
			p = g.compare(grid, p, x, y, -1, 0)
			grid[y][x] = p
		}
	}
	return grid
}

func (g *SDFGenerator) GenerateFromImage(path string) *image.RGBA {
	file, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		log.Fatal(err)
	}

	// 新增：统一转换为RGBA格式
	rgbaImg := image.NewRGBA(img.Bounds())
	draw.Draw(rgbaImg, img.Bounds(), img, image.Point{}, draw.Src)

	bounds := rgbaImg.Bounds()
	g.WIDTH = bounds.Dx()
	g.HEIGHT = bounds.Dy()

	log.Print("图片大小: ", g.WIDTH, g.HEIGHT)

	// 初始化网格
	g.grid1 = make([][]Point, g.HEIGHT)
	g.grid2 = make([][]Point, g.HEIGHT)

	for y := 0; y < g.HEIGHT; y++ {
		g.grid1[y] = make([]Point, g.WIDTH)
		g.grid2[y] = make([]Point, g.WIDTH)
		for x := 0; x < g.WIDTH; x++ {
			// 修改颜色提取方式
			_, gVal, _, _ := rgbaImg.At(x, y).RGBA()
			green := uint8(gVal >> 8)

			// grid1:物体外到物体的距离，grid2:物体内到物体的距离
			if green < 128 {
				g.grid1[y][x] = g.empty
				g.grid2[y][x] = g.inside
			} else {
				g.grid1[y][x] = g.inside
				g.grid2[y][x] = g.empty
			}
		}
	}

	// 使用协程并发处理
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		g.grid1 = g.generateSDF(g.grid1)
		wg.Done()
	}()

	wg.Add(1)
	go func() {
		g.grid2 = g.generateSDF(g.grid2)
		wg.Done()
	}()

	wg.Wait()

	// 修改图像类型为RGBA
	sdf := image.NewRGBA(image.Rect(0, 0, g.WIDTH, g.HEIGHT))

	for y := 0; y < g.HEIGHT; y++ {
		for x := 0; x < g.WIDTH; x++ {
			dist1 := math.Sqrt(float64(g.grid1[y][x].distSq()))
			dist2 := math.Sqrt(float64(g.grid2[y][x].distSq()))

			// 计算有符号距离
			dist := (dist1 - dist2)

			// 使用图像高度进行自适应归一化
			maxExpectedDist := float64(g.HEIGHT) / 6 // 经验值，可以调整
			normalizedDist := dist / maxExpectedDist

			// 将[-1, 1]范围映射到[0, 1]
			value := uint8(math.Max(0, math.Min(255, (normalizedDist+1)*127.5)))

			sdf.Set(x, y, color.RGBA{
				R: value,
				G: value,
				B: value,
				A: 255,
			})
		}
	}
	return sdf
}

func (g *SDFGenerator) BlendSDF(sdfs []*image.RGBA, outputPath string) {
	if len(sdfs) < 2 {
		log.Fatal("至少需要两张SDF图进行混合")
	}

	bounds := sdfs[0].Bounds()
	sum := make([][]int, bounds.Dy()) // 单通道累加器
	for y := range sum {
		sum[y] = make([]int, bounds.Dx())
	}

	// 只需处理R通道，因RGB相同
	for i := 0; i < len(sdfs)-1; i++ {
		startTime := time.Now()
		blended := g.blendPair(sdfs[i], sdfs[i+1])
		log.Printf("混合耗时 %d-%d: %v", i, i+1, time.Since(startTime))

		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				c := blended.RGBAAt(x, y)
				sum[y][x] += int(c.R) // 仅累加红色通道
			}
		}
	}

	result := image.NewRGBA(bounds)
	count := len(sdfs) - 1
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			avg := uint8(math.Min(255, math.Max(0, float64(sum[y][x])/float64(count))))
			// 三通道赋相同值
			result.SetRGBA(x, y, color.RGBA{
				R: avg,
				G: avg,
				B: avg,
				A: 255,
			})
		}
	}

	f, _ := os.Create(outputPath)
	defer f.Close()
	png.Encode(f, result)
	log.Printf("生成混合图像: %s (混合了%d对相邻图)", outputPath, count)
}

// 新增的配对混合方法
func (g *SDFGenerator) blendPair(a, b *image.RGBA) *image.RGBA {
	blended := image.NewRGBA(a.Bounds())
	draw.Draw(blended, blended.Bounds(), a, image.Point{}, draw.Src)

	temp := image.NewRGBA(blended.Bounds())

	for y := blended.Bounds().Min.Y; y < blended.Bounds().Max.Y; y++ {
		for x := blended.Bounds().Min.X; x < blended.Bounds().Max.X; x++ {
			c1 := blended.RGBAAt(x, y)
			c2 := b.RGBAAt(x, y)

			sdfA := float64(c1.R)/255.0*2.0 - 1.0
			sdfB := float64(c2.R)/255.0*2.0 - 1.0

			var result float64
			if sdfA < 0.0 {
				result = 1.0
			} else if sdfB > 0.0 {
				result = 0.0
			} else if sdfA > 0 && sdfB < 0 {
				distA := math.Abs(sdfA)
				distB := math.Abs(sdfB)
				result = distB / (distA + distB)
			} else {
				result = 0.0
			}

			value := uint8((result) * 255)
			temp.SetRGBA(x, y, color.RGBA{
				R: value,
				G: value,
				B: value,
				A: 255,
			})
		}
	}
	return temp
}

// 通配符处理函数，返回匹配的图片文件列表
func expandGlob(patterns []string) []string {
	var files []string
	validExts := map[string]bool{
		".png":  true,
		".jpg":  true,
		".jpeg": true,
		".gif":  true,
		".bmp":  true,
	}

	for _, pattern := range patterns {
		// 如果没有指定扩展名，自动添加图片扩展名匹配
		if !strings.Contains(pattern, ".") {
			pattern = pattern + ".{png,jpg,jpeg,gif,bmp}"
		}

		matches, err := filepath.Glob(pattern)
		if err != nil {
			log.Printf("警告: 无效的路径模式: %s (%v)", pattern, err)
			continue
		}

		// 验证文件类型
		for _, match := range matches {
			ext := strings.ToLower(filepath.Ext(match))
			if !validExts[ext] {
				continue
			}

			// 验证文件是否存在且可访问
			if info, err := os.Stat(match); err != nil || info.IsDir() {
				continue
			}

			files = append(files, match)
		}
	}

	if len(files) == 0 {
		log.Fatal("未找到匹配的图片文件，支持的格式：PNG、JPG、JPEG、GIF、BMP")
	}

	return files
}

// 修改main函数中的blend命令处理
func main() {
	// 定义命令行标志
	genCmd := flag.NewFlagSet("gen", flag.ExitOnError)
	blendCmd := flag.NewFlagSet("blend", flag.ExitOnError)

	// gen命令参数
	genOutput := genCmd.String("o", "sdf_output", "指定SDF图输出目录路径")
	genHelp := genCmd.Bool("h", false, "显示gen命令的帮助信息")

	// blend命令参数
	blendOutput := blendCmd.String("o", "blended.png", "指定混合结果输出路径")
	blendHelp := blendCmd.Bool("h", false, "显示blend命令的帮助信息")

	if len(os.Args) < 2 {
		fmt.Println("可用命令:")
		fmt.Println("  gen <输入图片>    - 生成SDF图")
		fmt.Println("  blend <图1> <图2> - 混合SDF图")
		os.Exit(1)
	}

	switch os.Args[1] {

	// 修改gen命令处理部分
	case "gen":
		genCmd.Parse(os.Args[2:])

		// 显示帮助信息
		if *genHelp {
			fmt.Println("用法: gen [选项] <输入图片...>")
			fmt.Println("\n选项:")
			genCmd.PrintDefaults()
			fmt.Println("\n说明:")
			fmt.Println("  输入图片支持通配符匹配，如 *.png")
			os.Exit(0)
		}

		// 获取非选项参数作为输入文件
		inputs := expandGlob(genCmd.Args())
		if len(inputs) == 0 {
			log.Fatal("请指定输入图片路径（支持通配符）")
		}

		// 使用-o参数指定的输出目录，如果未指定则使用默认值
		outputDir := *genOutput
		if outputDir == "" {
			outputDir = "sdf_output"
		}
		log.Printf("使用输出目录: %s, %v", outputDir, inputs)
		os.MkdirAll(outputDir, 0755)

		generator := NewSDFGenerator()

		for _, inputFile := range inputs {
			// 只使用文件名
			fileName := filepath.Base(inputFile)
			baseName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
			output := filepath.Join(outputDir, baseName+".sdf.png")

			processStart := time.Now()
			sdf := generator.GenerateFromImage(inputFile)
			log.Printf("生成耗时 %s: %v", inputFile, time.Since(processStart))

			f, err := os.Create(output)
			if err != nil {
				log.Fatalf("无法创建文件: %v", err)
			}
			png.Encode(f, sdf)
			f.Close()
			log.Printf("已保存SDF图: %s", output)
		}

	// 修改blend命令处理部分
	case "blend":
		blendCmd.Parse(os.Args[2:])

		// 显示帮助信息
		if *blendHelp {
			fmt.Println("用法: blend [选项] <SDF图1> <SDF图2> [SDF图3...]")
			fmt.Println("\n选项:")
			blendCmd.PrintDefaults()
			fmt.Println("\n说明:")
			fmt.Println("  至少需要两张SDF图进行混合")
			fmt.Println("  输入图片支持通配符匹配，如 *.sdf.png")
			os.Exit(0)
		}

		inputs := expandGlob(blendCmd.Args())
		if len(inputs) < 2 {
			log.Fatal("请指定至少两张要混合的SDF图路径（支持通配符）")
		}

		// 创建输出目录
		outputPath := *blendOutput
		os.MkdirAll(filepath.Dir(outputPath), 0755)

		generator := NewSDFGenerator()

		// 加载所有SDF图（保持原有校验逻辑）
		var sdfList []*image.RGBA
		for _, path := range inputs {
			f, err := os.Open(path)
			if err != nil {
				log.Fatal(err)
			}

			img, err := png.Decode(f)
			f.Close()

			if rgbaImg, ok := img.(*image.RGBA); ok {
				// 检查尺寸一致性
				if len(sdfList) > 0 && !rgbaImg.Bounds().Eq(sdfList[0].Bounds()) {
					log.Fatalf("图像尺寸不一致: %s (%v) vs %s (%v)",
						path, rgbaImg.Bounds(), inputs[0], sdfList[0].Bounds())
				}
				sdfList = append(sdfList, rgbaImg)
			} else {
				log.Fatalf("文件格式错误: %s", path)
			}
		}

		os.MkdirAll(filepath.Dir(*blendOutput), 0755)
		generator.BlendSDF(sdfList, *blendOutput)
		log.Printf("已生成混合图: %s", *blendOutput)

	default:
		log.Fatal("无效命令，可用命令: gen, blend")
	}
}
