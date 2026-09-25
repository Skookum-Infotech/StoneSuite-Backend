package docpdf

import (
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// maxDarkShare bounds the fraction of the logo's opaque pixels that may be
// near-black: a light wordmark keeps this near zero, so the test fails if the
// dark-on-light variant (~45% dark, measured on the real asset) is ever
// embedded here by mistake -- it would vanish on the ink band.
const maxDarkShare = 0.05

func TestStoneSuiteLogoAsset(t *testing.T) {
	img, err := png.Decode(strings.NewReader(stoneSuiteLogoWhite))
	require.NoError(t, err, "the embedded StoneSuite logo must be a valid PNG")
	b := img.Bounds()
	ratio := float64(b.Dx()) / float64(b.Dy())
	assert.Greater(t, ratio, 2.0, "the lockup is wider than tall")
	assert.Less(t, ratio, 4.0, "the padding around the lockup is trimmed")

	// The logo is drawn directly on the dark ink band (no plate), so its
	// wordmark must be light.
	var opaque, dark int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA() // 16-bit, alpha-premultiplied
			if a < 0xc000 {
				continue
			}
			opaque++
			if (2126*r+7152*g+722*bl)/10000 < 0x4000 {
				dark++
			}
		}
	}
	require.Positive(t, opaque, "the logo has visible pixels")
	assert.Less(t, float64(dark)/float64(opaque), maxDarkShare, "the wordmark is light, so it reads on the ink band")
}

// imagePlacement is one image drawn on the page: its left edge and width, in mm.
type imagePlacement struct{ x, w float64 }

var imageOpRE = regexp.MustCompile(`q ([\d.]+) 0 0 ([\d.]+) ([\d.]+) ([\d.]+) cm /I\S+ Do Q`)

// imagePlacements lists the images the PDF draws, in drawing order.
func imagePlacements(out []byte) []imagePlacement {
	var ps []imagePlacement
	for _, m := range imageOpRE.FindAllStringSubmatch(pdfText(out), -1) {
		w, _ := strconv.ParseFloat(m[1], 64)
		x, _ := strconv.ParseFloat(m[3], 64)
		ps = append(ps, imagePlacement{x: x / ptPerMM, w: w / ptPerMM})
	}
	return ps
}

func TestRender_Logos(t *testing.T) {
	t.Run("the StoneSuite logo sits at the left margin", func(t *testing.T) {
		out, err := Render(sampleDoc())
		require.NoError(t, err)
		ps := imagePlacements(out)
		require.Len(t, ps, 1)
		assert.InDelta(t, marginX, ps[0].x, alignDeltaM)
	})

	t.Run("the tenant's logo follows it with a gap, no plate", func(t *testing.T) {
		d := sampleDoc()
		d.Seller.LogoPNG = samplePNGBytes(t)
		out, err := Render(d)
		require.NoError(t, err)
		ps := imagePlacements(out)
		require.Len(t, ps, 2, "the StoneSuite logo, then the tenant's")
		brand, tenant := ps[0], ps[1]
		assert.InDelta(t, marginX, brand.x, alignDeltaM)
		assert.InDelta(t, logoGap, tenant.x-(brand.x+brand.w), alignDeltaM, "just the gap, no plate padding")
	})
}

func TestDrawLogo(t *testing.T) {
	tests := []struct {
		name   string
		logo   []byte
		wantOK bool
	}{
		{"usable PNG", samplePNGBytes(t), true},
		{"not an image", []byte("not a real image"), false},
		{"empty", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pdf := newDoc()
			end, ok := drawLogo(pdf, imgTenant, tc.logo, marginX)
			assert.Equal(t, tc.wantOK, ok)
			assert.True(t, pdf.Ok(), "a bad logo must never poison the rest of the PDF")
			if tc.wantOK {
				assert.Greater(t, end, marginX, "a decoded logo takes some room")
				assert.LessOrEqual(t, end-marginX, logoMaxW+0.001, "the logo never outgrows its box")
			} else {
				assert.InDelta(t, marginX, end, 0.0001, "a bad logo takes no room")
			}
		})
	}
}

func TestDrawMasthead(t *testing.T) {
	tests := []struct {
		name       string
		logo       []byte
		wantTenant bool
	}{
		{"no tenant logo", nil, false},
		{"valid tenant logo", samplePNGBytes(t), true},
		{"invalid tenant logo", []byte("not a real image"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := sampleDoc()
			d.Seller.LogoPNG = tc.logo
			pdf := newDoc()
			bottom := drawMasthead(pdf, d)
			assert.True(t, pdf.Ok(), "a bad logo must never poison the rest of the PDF")
			assert.InDelta(t, bandH+accentH, bottom, 0.001)
			assert.NotNil(t, pdf.GetImageInfo(imgStoneSuite), "the StoneSuite mark is always drawn")
			assert.Equal(t, tc.wantTenant, pdf.GetImageInfo(imgTenant) != nil)
		})
	}
}

func TestFitFontSize(t *testing.T) {
	const long = "PURCHASE ORDER RETURN AUTHORIZATION 0000000000"
	tests := []struct {
		name    string
		text    string
		maxW    float64
		wantMax bool // expect the maximum size
		wantMin bool // expect the minimum size
	}{
		{"short text keeps the maximum size", "INV", 100, true, false},
		{"long text shrinks but stays above the minimum", "PURCHASE ORDER", 45, false, false},
		{"text that can never fit floors at the minimum", long, 5, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pdf := newDoc()
			got := fitFontSize(pdf, tc.text, "B", kindMaxPt, kindMinPt, tc.maxW)
			assert.GreaterOrEqual(t, got, kindMinPt)
			assert.LessOrEqual(t, got, kindMaxPt)
			switch {
			case tc.wantMax:
				assert.Equal(t, kindMaxPt, got)
			case tc.wantMin:
				assert.Equal(t, kindMinPt, got)
			default:
				assert.Less(t, got, kindMaxPt)
				assert.Greater(t, got, kindMinPt)
				pdf.SetFont(fontFace, "B", got)
				assert.LessOrEqual(t, pdf.GetStringWidth(tc.text), tc.maxW, "the returned size must actually fit")
			}
		})
	}
}
