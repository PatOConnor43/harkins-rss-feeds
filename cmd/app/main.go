package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Session represents a single movie showtime
type Session struct {
	TheatreID      int      `json:"theatreId"`
	MovieID        string   `json:"movieId"`
	ShowtimeDate   string   `json:"showtimeDate"`
	ShowtimeOffset string   `json:"showtimeOffset"`
	Attributes     []string `json:"attributes"`
	SoldOut        bool     `json:"soldOut"`
	Format         string   `json:"format"`
	MPAARating     string   `json:"mpaaRating"`
	TicketingURL   string   `json:"ticketingUrl"`
	Genres         []string `json:"genres"`
}

// The JSON structure in the script is a map of MovieID -> Map of Date -> []Session
type ShowingsMap map[string]map[string][]Session

func fetchTheatreSlugs() ([]string, error) {
	resp, err := http.Get("https://cmsservice.harkins.com/api/v1/theaters")
	if err != nil {
		return nil, fmt.Errorf("error fetching theatres: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error: received status code %d", resp.StatusCode)
	}

	var theatreData []struct {
		Theatres []struct {
			SlugURL string `json:"slugUrl"`
		} `json:"theatres"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&theatreData); err != nil {
		return nil, fmt.Errorf("error decoding theatres JSON: %w", err)
	}

	var slugs []string
	for _, group := range theatreData {
		for _, theatre := range group.Theatres {
			if theatre.SlugURL != "" {
				slugs = append(slugs, theatre.SlugURL)
			}
		}
	}
	return slugs, nil
}

func generateFeed(theatreSlug, date string, movies Movies) error {
	url := fmt.Sprintf("https://www.harkins.com/theatres/%s/%s", theatreSlug, date)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("error making request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("error: received status code %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading body: %w", err)
	}

	// Use regex to find the content of <script id="__NEXT_DATA__">...</script>
	re := regexp.MustCompile(`(?s)<script id="__NEXT_DATA__"[^>]*>(.*?)</script>`)
	matches := re.FindStringSubmatch(string(bodyBytes))

	if len(matches) < 2 {
		return fmt.Errorf("could not find __NEXT_DATA__ tag for %s", theatreSlug)
	}

	jsonContent := strings.TrimSpace(matches[1])

	var fullData map[string]interface{}
	if err := json.Unmarshal([]byte(jsonContent), &fullData); err != nil {
		return fmt.Errorf("error unmarshaling __NEXT_DATA__: %w", err)
	}

	// Extract sessions from the nested structure
	var sessions []Session
	if props, ok := fullData["props"].(map[string]interface{}); ok {
		if pageProps, ok := props["pageProps"].(map[string]interface{}); ok {
			if performances, ok := pageProps["performances"].(map[string]interface{}); ok {
				for _, moviePerformances := range performances {
					if movieData, ok := moviePerformances.(map[string]interface{}); ok {
						for _, datePerformances := range movieData {
							if sessionsList, ok := datePerformances.([]interface{}); ok {
								for _, sessionItem := range sessionsList {
									sessionBytes, err := json.Marshal(sessionItem)
									if err != nil {
										continue
									}
									var session Session
									if err := json.Unmarshal(sessionBytes, &session); err != nil {
										continue
									}
									sessions = append(sessions, session)
								}
							}
						}
					}
				}
			}
		}
	}

	// Build lookups from movie ID to title and synopsis
	movieByID := make(map[string]struct {
		Title    string
		Synopsis string
	})
	for _, movie := range movies {
		movieByID[movie.MovieID] = struct {
			Title    string
			Synopsis string
		}{Title: movie.Title, Synopsis: movie.Synopsis}
	}

	// Collect unique movie IDs from sessions
	seenMovies := make(map[string]bool)
	var movieIDs []string
	for _, session := range sessions {
		if !seenMovies[session.MovieID] {
			seenMovies[session.MovieID] = true
			movieIDs = append(movieIDs, session.MovieID)
		}
	}

	// Build description with each movie title and synopsis
	var descParts []string
	for _, movieID := range movieIDs {
		info, ok := movieByID[movieID]
		if !ok {
			continue
		}
		descParts = append(descParts, fmt.Sprintf("<h3>%s</h3><p>%s</p>", info.Title, info.Synopsis))
	}
	sort.Strings(descParts)
	description := strings.Join(descParts, "\n\n")

	theatreURL := fmt.Sprintf("https://www.harkins.com/theatres/%s/%s", theatreSlug, date)

	feed := RSSFeed{
		Version: "2.0",
		Channel: RSSChannel{
			Title:       fmt.Sprintf("Harkins Showtimes - %s", theatreSlug),
			Link:        theatreURL,
			Description: fmt.Sprintf("Movie showtimes at Harkins %s", theatreSlug),
			Items: []RSSItem{
				{
					Title:       fmt.Sprintf("Movies playing on %s", date),
					Link:        theatreURL,
					Description: CDATA{Value: description},
					GUID:        RSSGUID{Value: theatreURL, IsPermaLink: true},
				},
			},
		},
	}

	xmlBytes, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling RSS: %w", err)
	}

	rssContent := xml.Header + string(xmlBytes)
	outputPath := fmt.Sprintf("rss/%s.xml", theatreSlug)
	if err := os.WriteFile(outputPath, []byte(rssContent), 0644); err != nil {
		return fmt.Errorf("error writing RSS file: %w", err)
	}

	fmt.Printf("RSS feed written to %s\n", outputPath)
	return nil
}

func main() {
	date := time.Now().Format("2006-01-02")

	// Fetch movies
	resp, err := http.Get("https://cmsservice.harkins.com/api/v1/movies")
	if err != nil {
		fmt.Printf("Error making request: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("Error: received status code %d\n", resp.StatusCode)
		os.Exit(1)
	}

	movies := Movies{}
	if err := json.NewDecoder(resp.Body).Decode(&movies); err != nil {
		fmt.Printf("Error decoding JSON: %v\n", err)
		os.Exit(1)
	}

	// Determine theatre slugs
	var slugs []string
	if len(os.Args) >= 2 {
		slugs = []string{os.Args[1]}
	} else {
		var err error
		slugs, err = fetchTheatreSlugs()
		if err != nil {
			fmt.Printf("Error fetching theatre slugs: %v\n", err)
			os.Exit(1)
		}
	}

	for _, slug := range slugs {
		fmt.Printf("Generating feed for %s...\n", slug)
		if err := generateFeed(slug, date, movies); err != nil {
			fmt.Printf("Error generating feed for %s: %v\n", slug, err)
		}
	}
}

type Movies []struct {
	SfID                 string `json:"sfId"`
	MovieID              string `json:"movieId"`
	Title                string `json:"title"`
	SlugURL              string `json:"slugUrl"`
	SortTitle            string `json:"sortTitle"`
	Synopsis             string `json:"synopsis"`
	Featured             bool   `json:"featured"`
	Runtime              int    `json:"runtime"`
	RuntimeFormatted     string `json:"runtimeFormatted"`
	Rating               string `json:"rating"`
	Advisory             string `json:"advisory"`
	EventType            string `json:"eventType"`
	EventTypeID          int    `json:"eventTypeId"`
	TrailerYoutubeURL    string `json:"trailerYoutubeUrl"`
	Imax                 bool   `json:"imax"`
	In3D                 bool   `json:"in3D"`
	ArtInd               bool   `json:"artInd"`
	Atmos                bool   `json:"atmos"`
	AudioDescription     bool   `json:"audioDescription"`
	ClosedCaption        bool   `json:"closedCaption"`
	ComingSoon           bool   `json:"comingSoon"`
	Exclusive            bool   `json:"exclusive"`
	Hfr                  bool   `json:"hfr"`
	NowShowing           bool   `json:"nowShowing"`
	MaxSeatsPerPurchase  int    `json:"maxSeatsPerPurchase"`
	ScheduleAvailable    bool   `json:"scheduleAvailable"`
	PresaleAvailable     bool   `json:"presaleAvailable"`
	TicketsAvailable     bool   `json:"ticketsAvailable"`
	Tnc                  bool   `json:"tnc"`
	Cine1                bool   `json:"cine1"`
	Cine1XL              bool   `json:"cine1XL"`
	CineCapri            bool   `json:"cineCapri"`
	OpenCaption          bool   `json:"openCaption"`
	Subtitled            bool   `json:"subtitled"`
	SensoryFriendly      bool   `json:"sensoryFriendly"`
	ForeignCinema        bool   `json:"foreignCinema"`
	EventAndSeries       bool   `json:"eventAndSeries"`
	HexCodeRightGradient string `json:"hexCodeRightGradient"`
	Directors            []struct {
		Item      string `json:"item"`
		SortOrder string `json:"sortOrder"`
		Title     string `json:"title"`
	} `json:"directors"`
	CastMembers []struct {
		Item      string `json:"item"`
		SortOrder string `json:"sortOrder"`
		Title     string `json:"title"`
	} `json:"castMembers"`
	Distributors []struct {
		Item      string `json:"item"`
		SortOrder string `json:"sortOrder"`
		Title     string `json:"title"`
	} `json:"distributors"`
	Genres []struct {
		Item      string `json:"item"`
		SortOrder string `json:"sortOrder"`
		Title     string `json:"title"`
	} `json:"genres"`
	Producers []struct {
		Item      string `json:"item"`
		SortOrder string `json:"sortOrder"`
		Title     string `json:"title"`
	} `json:"producers"`
	Writers      []any `json:"writers"`
	ReleaseDates []struct {
		Date    string `json:"date"`
		Pattern string `json:"pattern"`
	} `json:"releaseDates"`
	ReleaseDate      time.Time `json:"releaseDate"`
	BehindTheScreens []any     `json:"behindTheScreens"`
	MovieImages      []any     `json:"movieImages"`
	MovieTrailers    []any     `json:"movieTrailers"`
	ImgUrls          []struct {
		Description  string `json:"description"`
		ImageType    string `json:"imageType"`
		URL          string `json:"url"`
		ThumbnailURL string `json:"thumbnailUrl"`
		AltTag       string `json:"altTag"`
	} `json:"imgUrls"`
	AdditionalUrls []struct {
		Sequence    int    `json:"sequence"`
		Description string `json:"description"`
		URL         string `json:"url"`
	} `json:"additionalUrls"`
	Attributes     []string `json:"attributes"`
	ChildMoviesIds []string `json:"childMoviesIds,omitempty"`
	OgTags         struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"ogTags"`
	PriorityMovie  bool `json:"priorityMovie"`
	DataProperties struct {
		Status                int       `json:"status"`
		IsDeleted             bool      `json:"isDeleted"`
		Visible               bool      `json:"visible"`
		ApprovalWorkflowState string    `json:"approvalWorkflowState"`
		IsPublished           bool      `json:"isPublished"`
		LastModified          time.Time `json:"lastModified"`
	} `json:"dataProperties"`
	CustomSeatmapSettings struct {
		RockerFillColor           string `json:"rockerFillColor"`
		LoungerFillColor          string `json:"loungerFillColor"`
		SelectedRockerFillColor   string `json:"selectedRockerFillColor"`
		SelectedLoungerFillColor  string `json:"selectedLoungerFillColor"`
		SelectedSeatFontColor     string `json:"selectedSeatFontColor"`
		DisplaySelectedSeatNumber bool   `json:"displaySelectedSeatNumber"`
		UnavailableSeatColor      string `json:"unavailableSeatColor"`
		SelectedSeatIcon          any    `json:"selectedSeatIcon"`
		UnavailableSeatIcon       any    `json:"unavailableSeatIcon"`
	} `json:"customSeatmapSettings"`
	CustomSeatmapColors struct {
		RockerFillColor           string `json:"rockerFillColor"`
		LoungerFillColor          string `json:"loungerFillColor"`
		SelectedRockerFillColor   string `json:"selectedRockerFillColor"`
		SelectedLoungerFillColor  string `json:"selectedLoungerFillColor"`
		SelectedSeatFontColor     string `json:"selectedSeatFontColor"`
		DisplaySelectedSeatNumber bool   `json:"displaySelectedSeatNumber"`
		UnavailableSeatColor      string `json:"unavailableSeatColor"`
		SelectedSeatIcon          any    `json:"selectedSeatIcon"`
		UnavailableSeatIcon       any    `json:"unavailableSeatIcon"`
	} `json:"customSeatmapColors"`
}

// RSS feed types
type RSSFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel RSSChannel `xml:"channel"`
}

type RSSChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []RSSItem `xml:"item"`
}

type RSSItem struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	Description CDATA   `xml:"description"`
	GUID        RSSGUID `xml:"guid"`
}

type CDATA struct {
	Value string `xml:",cdata"`
}

type RSSGUID struct {
	Value       string `xml:",chardata"`
	IsPermaLink bool   `xml:"isPermaLink,attr"`
}
