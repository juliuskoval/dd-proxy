package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	gprof "github.com/google/pprof/profile"
	"github.com/klauspost/compress/zstd"
)

type Events struct {
	End          time.Time `json:"end"`
	Start        time.Time `json:"start"`
	TagsProfiler string    `json:"tags_profiler"`
}

func main() {
	http.ListenAndServe(":8126", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		multiPartReader, err := r.MultipartReader()
		if err != nil {
			return
		}
		e := &Events{}
		pprof := []byte{}

		for {
			part, err := multiPartReader.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(part)
			if part.FormName() == "event.json" || part.FormName() == "event" || part.FileName() == "event.json" || part.FileName() == "event" {
				if err := json.Unmarshal(data, e); err != nil {
					fmt.Printf("err: %v\n", err)
				}
			} else if strings.HasSuffix(part.FileName(), ".pprof") || strings.HasSuffix(part.FormName(), ".pprof") {
				pprof = data
			}
		}

		pprof, err = fixpprof(pprof)
		if err != nil {
			return
		}

		name := flameql(e.TagsProfiler)
		u, _ := url.Parse("http://localhost:4040/ingest")
		q := u.Query()
		q.Set("name", name)
		q.Set("format", "pprof")
		q.Set("from", fmt.Sprintf("%d", e.Start.Unix()))
		q.Set("until", fmt.Sprintf("%d", e.End.Unix()))
		u.RawQuery = q.Encode()
		fmt.Printf(">> request  start: %+v  end %+v  name: %+v \n ", e.Start, e.End, name)

		req, _ := http.NewRequest("POST", u.String(), bytes.NewReader(pprof))
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := http.DefaultClient.Do(req)
		var body = []byte{}
		if resp != nil && resp.Body != nil {
			body, _ = io.ReadAll(resp.Body)
		}
		if err != nil {
			fmt.Printf("      err: %v\n", err)
		} else {
			fmt.Printf("      resp: %s %s\n", resp.Status, body)
		}
	}))
}

func fixpprof(pprof []byte) ([]byte, error) {
	zstdReader, err := zstd.NewReader(bytes.NewReader(pprof))
	var p *gprof.Profile
	if err == nil {
		p, err = gprof.Parse(zstdReader)
		zstdReader.Close()
	}

	if err != nil {
		p, err = gprof.Parse(bytes.NewReader(pprof))
		if err != nil {
			return nil, err
		}
	}
	for _, valueType := range p.SampleType {
		if strings.Contains(valueType.Type, "-") {
			valueType.Type = strings.Replace(valueType.Type, "-", "_", -1)
		}
		if strings.Contains(valueType.Unit, "-") {
			valueType.Unit = strings.Replace(valueType.Unit, "-", "_", -1)
		}
	}
	fixedfunctions := map[*gprof.Function]struct{}{}

	for _, s := range p.Sample {

		fixfsample(s, fixedfunctions)
	}

	p = p.Compact()
	buffer := bytes.NewBuffer(nil)
	err = p.Write(buffer)
	return buffer.Bytes(), err
}

func fixfsample(s *gprof.Sample, fixedfunctions map[*gprof.Function]struct{}) {
	//s.Label = sanitizemap(s.Label)
	//s.NumLabel = sanitizemap(s.NumLabel)
	//s.NumUnit = sanitizemap(s.NumUnit)
	s.Label = nil
	s.NumLabel = nil
	s.NumUnit = nil
	for _, location := range s.Location {
		for _, line := range location.Line {
			if _, ok := fixedfunctions[line.Function]; ok {
				continue
			}
			fixedfunctions[line.Function] = struct{}{}
			pp := p{d: []byte(line.Function.Name), f: map2name}

			nn := pp.parse()

			fmt.Printf("-----------------%s\n => %s\n", line.Function.Name, nn)
			line.Function.Name = nn
		}
	}
}

func sanitizemap[T any](m map[string][]T) map[string][]T {
	newmap := make(map[string][]T)
	for k, v := range m {
		newmap[sanitize(k)] = v
	}
	return newmap
}

var re = regexp.MustCompile(`\W`)

func sanitize(s string) string {
	return re.ReplaceAllString(s, "_")
}
func flameql(tags string) string {

	tagslist := strings.Split(tags, ",")
	tagmap := make(map[string]string)

	for _, tag := range tagslist {
		parts := strings.Split(tag, ":")
		if len(parts) == 2 {
			tagmap[parts[0]] = parts[1]
		}
	}
	app := tagmap["service"]
	res := strings.Builder{}
	res.WriteString(sanitize(app))
	res.WriteString("{")
	cnt := 0
	for k, v := range tagmap {
		if k == "service" {
			continue
		}
		if cnt > 0 {
			res.WriteString(",")
		}
		res.WriteString(sanitize(k))
		res.WriteString("=\"")
		res.WriteString(sanitize(v))
		res.WriteString("\"")
		cnt++
	}
	res.WriteString("}")
	return res.String()
}

// function names (for dotnent?)
type p struct {
	d []byte
	i int
	f func(map[string]string) string
}

func (p *p) parse() string {
	k := ""
	v := ""
	kk := false
	vv := false
	kvmap := make(map[string]string)
	//debug := func(sss string) {
	//	fmt.Printf(" || %s\n", sss)
	//}
	prev := byte(' ')
	for p.i < len(p.d) {
		c := p.d[p.i]
		cc := string(c)
		_ = cc
		if c == '|' && prev == ' ' {
			kk = true
			vv = false
			p.i += 1
			continue
		}
		prev = c
		if c == ':' {
			kk = false
			vv = true
			p.i++
			continue
		}
		if c == ' ' {
			kvmap[k] = v
			k = ""
			v = ""
			kk = false
			vv = false
			p.i++

			continue
		}
		if c == '{' {
			p.i++
			v += "{"
			v += p.parse()
			v += "}"
			continue
		}
		if c == '}' {
			p.i++
			break
		}
		p.i++
		if kk {
			k += string(c)
		} else if vv {
			v += string(c)
		}

	}
	if k != "" && v != "" {
		kvmap[k] = v
	}
	return p.f(kvmap)
}

func map2name(kvmap map[string]string) string {
	ns := kvmap["ns"]
	lm := kvmap["lm"]
	fn := kvmap["fn"]
	ct := kvmap["ct"]
	if ns != "" && fn != "" && ct != "" {
		return fmt.Sprintf("%s!%s@%s", ns, ct, fn)
	}
	if lm != "" && fn != "" && ct != "" {
		return fmt.Sprintf("%s!%s@%s", lm, ct, fn)
	}

	if ct != "" && fn != "" {
		return fmt.Sprintf("%s@%s", ct, fn)
	}
	if ct != "" {
		return fmt.Sprintf("%s", ct)
	}
	return fmt.Sprintf("%v", kvmap)
}
