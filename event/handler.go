package event

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/concourse/atc/db"
	"github.com/concourse/turbine"
	"github.com/tedsuo/rata"
	"github.com/vito/go-sse/sse"
)

type BuildsDB interface {
	GetBuild(buildID int) (db.Build, error)
	GetBuildEvents(buildID int) ([]db.BuildEvent, error)
}

type Censor func(sse.Event) (sse.Event, error)

func NewHandler(buildsDB BuildsDB, buildID int, censor Censor) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		build, err := buildsDB.GetBuild(buildID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		flusher := w.(http.Flusher)

		w.Header().Add("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Add("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Add("Connection", "keep-alive")

		if build.Status == db.StatusStarted {
			generator := rata.NewRequestGenerator(build.Endpoint, turbine.Routes)

			events, err := generator.CreateRequest(
				turbine.GetBuildEvents,
				rata.Params{"guid": build.Guid},
				nil,
			)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			events.Header.Set("Last-Event-ID", r.Header.Get("Last-Event-ID"))

			resp, err := http.DefaultClient.Do(events)
			if err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}

			defer resp.Body.Close()

			w.WriteHeader(http.StatusOK)

			flusher.Flush()

			reader := sse.NewReader(resp.Body)

			for {
				ev, err := reader.Next()
				if err != nil {
					return
				}

				if censor != nil {
					ev, err = censor(ev)
					if err != nil {
						return
					}
				}

				err = ev.Write(w)
				if err != nil {
					return
				}

				flusher.Flush()
			}
		} else {
			events, err := buildsDB.GetBuildEvents(buildID)
			if err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}

			start := 0
			if r.Header.Get("Last-Event-ID") != "" {
				var err error

				lastEvent, err := strconv.Atoi(r.Header.Get("Last-Event-ID"))
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				start = lastEvent + 1
			}

			if start >= len(events) {
				return
			}

			for idx, event := range events[start:] {
				ev := sse.Event{
					ID:   fmt.Sprintf("%d", idx+start),
					Name: event.Type,
					Data: []byte(event.Payload),
				}

				if censor != nil {
					ev, err = censor(ev)
					if err != nil {
						return
					}
				}

				err := ev.Write(w)
				if err != nil {
					return
				}

				flusher.Flush()
			}
		}

		return
	})
}