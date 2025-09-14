package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	_ "github.com/tursodatabase/libsql-client-go/libsql"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	embedsql "github.com/mr-destructive/link-blog/embedsql"
	"github.com/mr-destructive/link-blog/models"
)

var (
	queries       *models.Queries
	listTemplate  *template.Template
	linkTemplate  *template.Template
	editTemplate  *template.Template
	detailTemplate *template.Template
)

// LinkMetadata represents metadata extracted from a webpage
type LinkMetadata struct {
	Title     string
	ImageURL  string
}

// fetchMetadata fetches metadata from a URL
func fetchMetadata(urlStr string) (LinkMetadata, error) {
	var metadata LinkMetadata
	
	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 10 * 1000 * 1000 * 1000, // 10 seconds
	}
	
	resp, err := client.Get(urlStr)
	if err != nil {
		return metadata, err
	}
	defer resp.Body.Close()
	
	// Parse HTML
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return metadata, err
	}
	
	// Extract title
	if title := doc.Find("title").Text(); title != "" {
		metadata.Title = strings.TrimSpace(title)
	} else if title, exists := doc.Find("meta[property='og:title']").Attr("content"); exists {
		metadata.Title = strings.TrimSpace(title)
	} else if title, exists := doc.Find("meta[name='title']").Attr("content"); exists {
		metadata.Title = strings.TrimSpace(title)
	}
	
	// Extract image
	if img, exists := doc.Find("meta[property='og:image']").Attr("content"); exists {
		metadata.ImageURL = img
	} else if img, exists := doc.Find("meta[name='twitter:image']").Attr("content"); exists {
		metadata.ImageURL = img
	} else {
		// Try to find first image in the page
		doc.Find("img").Each(func(i int, s *goquery.Selection) {
			if metadata.ImageURL == "" {
				if src, exists := s.Attr("src"); exists {
					// Handle relative URLs
					if strings.HasPrefix(src, "//") {
						metadata.ImageURL = "https:" + src
					} else if strings.HasPrefix(src, "/") {
						// Try to construct absolute URL
						if u, err := url.Parse(urlStr); err == nil {
							base := u.Scheme + "://" + u.Host
							metadata.ImageURL = base + src
						}
					} else if strings.HasPrefix(src, "http") {
						metadata.ImageURL = src
					}
				}
			}
		})
	}
	
	return metadata, nil
}

func main() {
	lambda.Start(handler)
}

func handler(req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {

	ctx := context.Background()
	dbName := os.Getenv("DB_NAME")
	dbToken := os.Getenv("DB_TOKEN")

	var err error
	dbString := fmt.Sprintf("libsql://%s?authToken=%s", dbName, dbToken)
	db, err := sql.Open("libsql", dbString)
	if err != nil {
		return events.APIGatewayProxyResponse{StatusCode: 500}, err
	}
	defer db.Close()

	queries = models.New(db)
	if _, err := db.ExecContext(ctx, embedsql.DDL); err != nil {
		log.Printf("error creating tables: %v", err)
		return events.APIGatewayProxyResponse{StatusCode: 500}, err
	}
	linkTemplate = template.Must(template.New("link").Parse(embedsql.LinkHTML))
	listTemplate = template.Must(template.New("list").Parse(embedsql.ListHTML))
	editTemplate = template.Must(template.New("edit").Parse(embedsql.EditHTML))
	detailTemplate = template.Must(template.New("detail").Parse(embedsql.DetailHTML))
	switch req.HTTPMethod {
	case http.MethodGet:
		if req.QueryStringParameters["id"] != "" {
			linkIdStr, ok := req.QueryStringParameters["id"]
			if !ok {
				return events.APIGatewayProxyResponse{StatusCode: 400, Body: "Missing link ID"}, nil
			}
			linkId, err := strconv.Atoi(linkIdStr)
			if err != nil {
				return events.APIGatewayProxyResponse{StatusCode: 400, Body: "Invalid link ID"}, nil
			}
			linkObj, err := queries.GetLink(ctx, int64(linkId))
			if err != nil {
				return events.APIGatewayProxyResponse{StatusCode: 500, Body: err.Error()}, nil
			}
			return respond(req, linkObj)
		}

		var links []models.Link
		links, err = queries.ListLinks(ctx)
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: 500}, err
		}
		return respond(req, links)
	case http.MethodPost:

		var link models.CreateLinkParams
		formData, err := url.ParseQuery(req.Body)
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: 400, Body: "Invalid request body"}, nil
		}
		Url := formData.Get("url")
		content := formData.Get("commentary")
		if content == "" || Url == "" {
			return events.APIGatewayProxyResponse{StatusCode: 400, Body: "Invalid request body"}, nil
		}
		link.Url = Url
		link.Commentary = content
		
		// Fetch metadata
		if metadata, err := fetchMetadata(Url); err == nil {
			link.Title = sql.NullString{String: metadata.Title, Valid: metadata.Title != ""}
			link.ImageUrl = sql.NullString{String: metadata.ImageURL, Valid: metadata.ImageURL != ""}
		}
		
		createdLinkId, err := queries.CreateLink(ctx, link)
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: 500, Body: err.Error()}, nil
		}
		createdLink, err := queries.GetLink(ctx, createdLinkId)
		return respond(req, createdLink)
	case http.MethodPut:
		linkIdStr := req.QueryStringParameters["id"]
		formData, err := url.ParseQuery(req.Body)
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: 400, Body: "Invalid request body"}, nil
		}
		if len(formData) == 0 && linkIdStr != "" {
			linkId, err := strconv.Atoi(linkIdStr)
			linkObj, err := queries.GetLink(ctx, int64(linkId))
			var tpl bytes.Buffer
			err = editTemplate.Execute(&tpl, linkObj)
			if err != nil {
				return events.APIGatewayProxyResponse{StatusCode: 500, Body: err.Error()}, nil
			}
			return events.APIGatewayProxyResponse{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "text/html"},
				Body:       tpl.String(),
			}, nil
		}
		linkId, err := strconv.Atoi(linkIdStr)
		linkObj, err := queries.GetLink(ctx, int64(linkId))
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: 400, Body: "Invalid link ID"}, nil
		}
		var link models.UpdateLinkParams
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: 400, Body: "Invalid request body"}, nil
		}
		Url := formData.Get("url")
		content := formData.Get("commentary")
		if content == "" || Url == "" {
			return events.APIGatewayProxyResponse{StatusCode: 400, Body: "Invalid request body"}, nil
		}
		if Url != "" && Url != linkObj.Url {
			link.Url = Url
		}
		link.ID = linkObj.ID
		link.Url = Url
		link.Commentary = content
		
		// Fetch metadata if URL changed
		if Url != linkObj.Url {
			if metadata, err := fetchMetadata(Url); err == nil {
				link.Title = sql.NullString{String: metadata.Title, Valid: metadata.Title != ""}
				link.ImageUrl = sql.NullString{String: metadata.ImageURL, Valid: metadata.ImageURL != ""}
			}
		} else {
			// Keep existing metadata
			link.Title = linkObj.Title
			link.ImageUrl = linkObj.ImageUrl
		}
		
		err = queries.UpdateLink(ctx, link)
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: 500, Body: err.Error()}, nil
		}
		linkObj, err = queries.GetLink(ctx, int64(linkId))
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: 500, Body: err.Error()}, nil
		}
		return respond(req, linkObj)

	case http.MethodDelete:
		linkIdStr, ok := req.QueryStringParameters["id"]
		if linkIdStr == "" {
			return events.APIGatewayProxyResponse{StatusCode: 400, Body: "Missing link ID"}, nil
		}
		if !ok {
			return events.APIGatewayProxyResponse{StatusCode: 400, Body: "Missing link"}, nil
		}
		linkId, err := strconv.Atoi(linkIdStr)
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: 400, Body: "Invalid link ID"}, nil
		}
		err = queries.DeleteLink(ctx, int64(linkId))
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: 500, Body: err.Error()}, nil
		}
		return events.APIGatewayProxyResponse{
			StatusCode: 200,
			Headers:    map[string]string{"Content-Type": "text/html"},
			Body:       "",
		}, nil
	default:
		return events.APIGatewayProxyResponse{StatusCode: 200}, err
	}
}

func respond(req events.APIGatewayProxyRequest, data any) (events.APIGatewayProxyResponse, error) {
	log.Printf("request headers: %v", req.Headers)

	if req.Headers["hx-request"] == "true" {
		var tpl bytes.Buffer

		switch v := data.(type) {
		case []models.Link:
			err := listTemplate.Execute(&tpl, v)
			if err != nil {
				return events.APIGatewayProxyResponse{StatusCode: 500}, err
			}
		case models.Link:
			// Check if this is a request for the detail view
			if req.QueryStringParameters["view"] == "detail" {
				err := detailTemplate.Execute(&tpl, v)
				if err != nil {
					return events.APIGatewayProxyResponse{StatusCode: 500}, err
				}
			} else {
				err := linkTemplate.Execute(&tpl, v)
				if err != nil {
					return events.APIGatewayProxyResponse{StatusCode: 500}, err
				}
			}
		default:
			return events.APIGatewayProxyResponse{StatusCode: 400}, fmt.Errorf("unsupported data type for HTML fragment generation: %T", data)
		}

		return events.APIGatewayProxyResponse{
			StatusCode: 200,
			Headers:    map[string]string{"Content-Type": "text/html"},
			Body:       tpl.String(),
		}, nil
	}

	dataBytes, err := json.Marshal(data)
	if err != nil {
		return events.APIGatewayProxyResponse{StatusCode: 500}, err
	}

	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       string(dataBytes),
	}, nil
}
