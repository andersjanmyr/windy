package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/buger/jsonparser"
	"github.com/fastly/compute-sdk-go/fsthttp"
	"github.com/fastly/compute-sdk-go/geo"
)

type wind struct {
	hour  string
	gust  float64
	speed float64
}

func main() {
	// Log service version
	fmt.Println("FASTLY_SERVICE_VERSION:", os.Getenv("FASTLY_SERVICE_VERSION"))
	fsthttp.ServeFunc(func(ctx context.Context, rw fsthttp.ResponseWriter, req *fsthttp.Request) {
		// Filter requests that have unexpected methods.
		if req.Method != "HEAD" && req.Method != "GET" {
			rw.WriteHeader(fsthttp.StatusMethodNotAllowed)
			fmt.Fprintf(rw, "This method is not allowed\n")
			return
		}
		ip := net.ParseIP(req.RemoteAddr)
		if ip == nil {
			rw.WriteHeader(fsthttp.StatusBadRequest)
			fmt.Fprintf(rw, "unable to parse the client IP %q\n", req.RemoteAddr)
			return
		}

		g, err := geo.Lookup(ip)
		if err != nil {
			rw.WriteHeader(fsthttp.StatusInternalServerError)
			fmt.Fprintf(rw, "unable to get client ip %q\n", err)
			return
		}
		if !strings.HasPrefix(req.URL.Path, "/wind") {
			fmt.Fprintf(rw, rootHTML(g))
			return
		}
		winds, err := fetchWinds(ctx, g)
		if err != nil {
			rw.WriteHeader(fsthttp.StatusBadGateway)
			fmt.Fprintln(rw, err)
			return
		}
		if req.URL.Path == "/wind.json" || req.URL.Path == "/wind" {
			rw.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(rw, "%s\n", toJSON(winds))
		}
		if req.URL.Path == "/wind.html" {
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(rw, "%s\n", toHTML(winds, g))

			return
		}
	})
}

func fetchWinds(ctx context.Context, g *geo.Geo) ([]wind, error) {
	body, err := sendRequest(ctx, "windspeed_10m,windgusts_10m", g)
	if err != nil {
		return nil, err
	}
	fmt.Printf("%s\n", string(body))
	times := parseString(body, "time")
	speeds := parseFloat(body, "windspeed_10m")
	gusts := parseFloat(body, "windgusts_10m")
	winds := make([]wind, len(times))
	for i := range speeds {
		winds[i].hour = times[i]
		winds[i].speed = speeds[i]
		winds[i].gust = gusts[i]
	}
	return winds, nil
}

func sendRequest(ctx context.Context, prop string, g *geo.Geo) ([]byte, error) {
	req, err := prepareRequest(prop, g)
	if err != nil {
		return nil, err
	}
	req.CacheOptions.TTL = 60 * 60 * 24
	resp, err := req.Send(ctx, "open-meteo")
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func prepareRequest(prop string, g *geo.Geo) (*fsthttp.Request, error) {
	u := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.2f&longitude=%.2f&windspeed_unit=ms&timezone=CET&hourly=%s", g.Latitude, g.Longitude, prop)
	fmt.Println(u)
	req, err := fsthttp.NewRequest("GET", u, nil)
	if err != nil {
		return req, err
	}
	return req, nil
}

func parseString(body []byte, prop string) []string {
	items := []string{}
	jsonparser.ArrayEach(body, func(value []byte, dataType jsonparser.ValueType, offset int, err error) {
		s, _ := jsonparser.ParseString(value)
		items = append(items, s)
	}, "hourly", prop)
	return items
}

func parseFloat(body []byte, prop string) []float64 {
	items := []float64{}
	jsonparser.ArrayEach(body, func(value []byte, dataType jsonparser.ValueType, offset int, err error) {
		f, err := jsonparser.ParseFloat(value)
		items = append(items, f)
	}, "hourly", prop)
	return items
}

func toJSON(winds []wind) string {
	ss := []string{}
	for _, w := range winds {
		ss = append(ss, fmt.Sprintf(`{"hour": "%s", "speed": %.2f, "gust": %.2f}`, w.hour, w.speed, w.gust))
	}
	return fmt.Sprintf("[\n%s\n]\n", strings.Join(ss, ",\n"))
}

func toHTML(winds []wind, g *geo.Geo) string {
	times := mapSlice(winds, func(w wind) string {
		return fmt.Sprintf("%q", w.hour)
	})
	speeds := mapSlice(winds, func(w wind) string {
		return fmt.Sprintf("%.2f", w.speed)
	})
	gusts := mapSlice(winds, func(w wind) string {
		return fmt.Sprintf("%.2f", w.gust)
	})
	timeStr := fmt.Sprintf("var times = [ %s ];", strings.Join(times, ", "))
	speedStr := fmt.Sprintf("var speeds = [ %s ];", strings.Join(speeds, ", "))
	gustStr := fmt.Sprintf("var gusts = [ %s ];", strings.Join(gusts, ", "))
	return fmt.Sprintf(`<html>
	<head>
	<title>Winds at lat: %.2[1]f, long: %.2[2]f</title>
	<script src="https://cdnjs.cloudflare.com/ajax/libs/Chart.js/2.9.4/Chart.js"></script>
	</head>
	<body>
	<h1>Winds at lat: %.2[1]f, long: %.2[2]f</h1>
	<canvas id="myChart" style="width:90%%;max-width:1024px;margin:1em"></canvas>

<script>
%[3]s
%[4]s
%[5]s
new Chart("myChart", {
  type: "line",
  data: {
	  labels: times,
	  datasets: [{
		  label: "Average",
		  data: speeds,
		  borderColor: "green",
		  fill: false
	  },{
		  label: "Gust",
		  data: gusts,
		  borderColor: "red",
		  fill: false
	  }]
  },
  options: {
	  title: {
		  display: true,
		  text: 'Wind speeds for %.2[1]f %.2[2]f'
	  }
  }
});
</script>
	</body>
	</html>`, g.Latitude, g.Longitude, timeStr, speedStr, gustStr)

}

func rootHTML(g *geo.Geo) string {
	return fmt.Sprintf(`<html>
	<head>
	<title>Winds at lat: %.2[1]f, long: %.2[2]f</title>
	</head>
	<body>
	<h1>Winds at lat: %.2[1]f, long: %.2[2]f</h1>
	<ul>
	<li><a href="/wind.html">Winds HTML</a></li>
	<li><a href="/wind.json">Winds JSON</a></li>
	</ul>
	</body>
	</html>`, g.Latitude, g.Longitude)
}

func mapSlice[T any, M any](a []T, f func(T) M) []M {
	n := make([]M, len(a))
	for i, e := range a {
		n[i] = f(e)
	}
	return n
}
