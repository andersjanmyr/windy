package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/buger/jsonparser"
	"github.com/fastly/compute-sdk-go/fsthttp"
	"github.com/fastly/compute-sdk-go/geo"
)

type entry struct {
	hour  string
	gust  float64
	speed float64
	price float64
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
		lat := req.URL.Query().Get("lat")
		long := req.URL.Query().Get("long")
		if lat == "" || long == "" {
			lat, long = fmt.Sprintf("%f", g.Latitude), fmt.Sprintf("%f", g.Longitude)
		}
		fmt.Println("latlong", lat, long)
		entries, err := fetchWinds(ctx, lat, long)
		prices, err := fetchPrices(ctx, "SE4")
		merge(entries, prices)
		if err != nil {
			rw.WriteHeader(fsthttp.StatusBadGateway)
			fmt.Fprintln(rw, err)
			return
		}
		if req.URL.Path == "/wind.json" {
			rw.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(rw, "%s\n", toJSON(entries))
		}
		if req.URL.Path == "/wind.html" {
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(rw, "%s\n", toHTML(entries, g, lat, long))

			return
		}
	})
}

func fetchWinds(ctx context.Context, lat, long string) ([]*entry, error) {
	body, err := sendRequest(ctx, "windspeed_10m,windgusts_10m", lat, long)
	if err != nil {
		return nil, err
	}
	times := parseString(body, "hourly", "time")
	speeds := parseFloat(body, "hourly", "windspeed_10m")
	gusts := parseFloat(body, "hourly", "windgusts_10m")
	max := 72
	entries := make([]*entry, max)
	for i := range times {
		if i == max {
			break
		}
		e := entry{
			hour:  times[i],
			speed: speeds[i],
			gust:  gusts[i],
		}
		entries[i] = &e
	}
	return entries, nil
}

func sendRequest(ctx context.Context, prop, lat, long string) ([]byte, error) {
	u := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.2s&longitude=%.2s&windspeed_unit=ms&timezone=CET&hourly=%s", lat, long, prop)
	fmt.Println(u)
	req, _ := fsthttp.NewRequest("GET", u, nil)
	req.CacheOptions.TTL = 60 * 60 * 1 // 1 hour
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

func merge(entries, prices []*entry) {
	for _, p := range prices {
		for _, e := range entries {
			if p.hour == e.hour {
				e.price = p.price
				break
			}
		}
	}
}

func fetchPrices(ctx context.Context, region string) ([]*entry, error) {
	today := time.Now()
	tomorrow := today.AddDate(0, 0, 1)
	eToday, err := fetchPrice(ctx, region, today)
	if err != nil {
		return nil, err
	}
	eTomorrow, err := fetchPrice(ctx, region, tomorrow)
	if err != nil {
		return nil, err
	}
	return append(eToday, eTomorrow...), nil
}

func fetchPrice(ctx context.Context, region string, t time.Time) ([]*entry, error) {
	body, err := sendPriceRequest(ctx, region, t)
	if err != nil {
		return nil, err
	}
	fmt.Printf("%s\n", string(body))
	entries := parsePrices(body)
	return entries, nil
}

func sendPriceRequest(ctx context.Context, region string, t time.Time) ([]byte, error) {
	// https://www.elprisetjustnu.se/api/v1/prices/2023/02-15_SE4.json
	u := fmt.Sprintf("https://www.elprisetjustnu.se/api/v1/prices/%d/%02d-%02d_%s.json", t.Year(), t.Month(), t.Day(), region)
	fmt.Println(u)
	req, _ := fsthttp.NewRequest("GET", u, nil)
	req.CacheOptions.TTL = 60 * 60 * 1 // 1 hour
	resp, err := req.Send(ctx, "elpris")
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

func parsePrices(body []byte) []*entry {
	items := []*entry{}
	jsonparser.ArrayEach(body, func(value []byte, dataType jsonparser.ValueType, offset int, err error) {
		s, _ := jsonparser.GetString(value, "time_start")
		f, _ := jsonparser.GetFloat(value, "SEK_per_kWh")
		e := &entry{}
		e.hour = s[0:16]
		e.price = f
		items = append(items, e)
	})
	return items
}

func parseString(body []byte, props ...string) []string {
	items := []string{}
	jsonparser.ArrayEach(body, func(value []byte, dataType jsonparser.ValueType, offset int, err error) {
		s, _ := jsonparser.ParseString(value)
		items = append(items, s)
	}, props...)
	return items
}

func parseFloat(body []byte, props ...string) []float64 {
	items := []float64{}
	jsonparser.ArrayEach(body, func(value []byte, dataType jsonparser.ValueType, offset int, err error) {
		f, err := jsonparser.ParseFloat(value)
		items = append(items, f)
	}, props...)
	return items
}

func toJSON(entries []*entry) string {
	ss := []string{}
	for _, e := range entries {
		ss = append(ss, fmt.Sprintf(`{"hour": "%s", "speed": %.2f, "gust": %.2f, "price": %.2f}`, e.hour, e.speed, e.gust, e.price))
	}
	return fmt.Sprintf("[\n%s\n]\n", strings.Join(ss, ",\n"))
}

func toHTML(entries []*entry, g *geo.Geo, lat, long string) string {
	times := mapSlice(entries, func(e *entry) string {
		d, t, _ := strings.Cut(e.hour, "T")
		h := t
		if t == "00:00" {
			h = d
		}
		return fmt.Sprintf("%q", h)
	})
	speeds := mapSlice(entries, func(e *entry) string {
		return fmt.Sprintf("%.2f", e.speed)
	})
	gusts := mapSlice(entries, func(e *entry) string {
		return fmt.Sprintf("%.2f", e.gust)
	})
	prices := mapSlice(entries, func(e *entry) string {
		return fmt.Sprintf("%.2f", e.price)
	})
	timeStr := fmt.Sprintf("var times = [ %s ];", strings.Join(times, ", "))
	speedStr := fmt.Sprintf("var speeds = [ %s ];", strings.Join(speeds, ", "))
	gustStr := fmt.Sprintf("var gusts = [ %s ];", strings.Join(gusts, ", "))
	priceStr := fmt.Sprintf("var prices = [ %s ];", strings.Join(prices, ", "))
	return fmt.Sprintf(`<html>
	<head>
	  <title>%[1]s</title>
	  <script src="https://cdnjs.cloudflare.com/ajax/libs/Chart.js/2.9.4/Chart.js"></script>
      <meta name="viewport" content="width=device-width, initial-scale=1">
	</head>
	<body>
	<h1>%[1]s</h1>
	<canvas id="myChart" style="width:90%%;max-width:1024px;margin:1em"></canvas>

<script>
%[2]s
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
	  },
	  {
		  label: "Gust",
		  data: gusts,
		  borderColor: "red",
		  fill: false
	  },
	  {
		  label: "Price",
		  data: prices,
		  borderColor: "blue",
		  fill: false
	  }]
  },
  options: {
	  title: {
		  display: true,
		  text: '%[1]s'
	  }
  }
});
</script>
	</body>
	</html>`,
		title(g, lat, long),
		timeStr, speedStr, gustStr, priceStr)

}

func title(g *geo.Geo, lat, long string) string {
	if lat != "" && long != "" {
		return fmt.Sprintf("Winds at browser location (lat: %.5[1]s, long: %.5[2]s)", lat, long)
	}
	return fmt.Sprintf("Winds in %[1]s, %[2]s (lat: %.2[3]f, long: %.2[4]f)",
		strings.Title(g.City), strings.Title(g.CountryName), g.Latitude, g.Longitude,
	)
}

func rootHTML(g *geo.Geo) string {
	return fmt.Sprintf(`<html>
	<head>
	  <title>%[1]s</title>
      <meta name="viewport" content="width=device-width, initial-scale=1">
	  <script>
	  function addGeo(link, coords) {
		  link.href = link.href + "?lat=" + coords.latitude + "&long=" + coords.longitude;
	  }
		if ("geolocation" in navigator) {
			  navigator.geolocation.getCurrentPosition((position) => {
				  const lat = position.coords.latitude;
				  const long = position.coords.longitude;
				  console.log("pos", lat, long);
				  const links = document.getElementsByClassName("wind");
				  console.log(links);
				  addGeo(links[0], position.coords)
				  addGeo(links[1], position.coords)
			  });
		}
		</script>
	</head>
	<body>
	<h1>%[1]s</h1>
	<ul>
	<li><a class="wind" href="/wind.html">Winds HTML</a></li>
	<li><a class="wind" href="/wind.json">Winds JSON</a></li>
	</ul>
	</body>
	</html>`, title(g, "", ""),
	)
}

func mapSlice[T any, M any](a []T, f func(T) M) []M {
	n := make([]M, len(a))
	for i, e := range a {
		n[i] = f(e)
	}
	return n
}
