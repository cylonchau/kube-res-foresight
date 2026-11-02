package main

// Reference examples are checked against the live parameter registry without contacting model services.
import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"regexp"
	"strings"

	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
)

func main() {
	count, params := 0, 0
	for _, d := range forecast.Definitions() {
		if d.ID == "formula" {
			continue
		}
		for _, lang := range []string{"zh", "en"} {
			data, err := os.ReadFile("website/content/" + lang + "/algorithm-" + d.ID + ".html")
			if err != nil {
				panic(err)
			}
			body := string(data)
			for _, p := range d.Params {
				if !strings.Contains(body, "<code>"+p.Name+"</code>") {
					panic(d.ID + ": missing " + p.Name)
				}
			}
			blocks := regexp.MustCompile(`(?s)<pre data-language="JSON"><code>(.*?)</code></pre>`).FindAllStringSubmatch(body, -1)
			if len(blocks) < 1 {
				panic(d.ID + ": example missing")
			}
			for _, block := range blocks {
				raw := map[string]any{}
				if err := json.Unmarshal([]byte(html.UnescapeString(block[1])), &raw); err != nil {
					panic(err)
				}
				m, err := forecast.Build(d.ID, raw)
				if err != nil {
					panic(fmt.Sprintf("%s %s: %v", lang, d.ID, err))
				}
				if m.MinSamples() < 1 {
					panic("invalid minimum")
				}
			}
		}
		count++
		params += len(d.Params)
	}
	fmt.Printf("PASS %d algorithms/services, %d parameter entries, all bilingual examples build with current validators\n", count, params)
}
