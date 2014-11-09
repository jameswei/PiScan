// Copyright Banrai LLC. All rights reserved. Use of this source code is
// governed by the license that can be found in the LICENSE file.

// Package ui provides http request handlers for the Pi client WebApp

package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/Banrai/PiScan/client/database"
	"github.com/mxk/go-sqlite/sqlite3"
	"html/template"
	"net/http"
	"path"
	"strconv"
)

var (
	ITEM_TEMPLATE_FILES = []string{"base.html", "navigation_tabs.html", "actions.html", "items.html", "modal.html"}
	TEMPLATE_LIST       = func(templatesFolder string, templateFiles []string) []string {
		t := make([]string, 0)
		for _, f := range templateFiles {
			t = append(t, path.Join(templatesFolder, f))
		}
		return t
	}
	ITEM_TEMPLATES        *template.Template
	TEMPLATES_INITIALIZED = false
)

// Use this to redirect one request to another target (string)
func Redirect(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// Respond to requests using HTML templates and the standard Content-Type (i.e., "text/html")
func MakeHTMLHandler(fn func(http.ResponseWriter, *http.Request, database.ConnCoordinates), db database.ConnCoordinates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fn(w, r, db)
	}
}

// Respond to requests that are not "text/html" Content-Types (e.g., for ajax calls)
func MakeHandler(fn func(*http.Request, database.ConnCoordinates) string, db database.ConnCoordinates, mediaType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", fmt.Sprintf("%s; charset=utf-8", mediaType))
		data := fn(r, db)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		fmt.Fprintf(w, data)
	}
}

/* JSON response struct */
type AjaxAck struct {
	Message string `json:"msg"`
	Error   string `json:"err,omitempty"`
}

/* HTML template structs */
type ActiveTab struct {
	Scanned   bool
	Favorites bool
	ShowTabs  bool
}

type Action struct {
	Icon   string
	Link   string
	Action string
}

type Page struct {
	Title     string
	ActiveTab *ActiveTab
	Actions   []*Action
	Items     []*database.Item
	Scanned   bool
	ShowItems bool
}

/* General db access functions */

// getItems returns a list of scanned or favorited products, and the correct
// corresponding options for the HTML page template
func getItems(w http.ResponseWriter, r *http.Request, dbCoords database.ConnCoordinates, favorites bool) {
	// attempt to connect to the db
	db, err := database.InitializeDB(dbCoords)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer db.Close()

	// get the Account for this request
	acc, accErr := database.GetDesignatedAccount(db)
	if accErr != nil {
		http.Error(w, accErr.Error(), http.StatusInternalServerError)
		return
	}

	// define the appropriate fetch item function
	fetch := func(db *sqlite3.Conn, acc *database.Account) ([]*database.Item, error) {
		if favorites {
			return database.GetFavoriteItems(db, acc)
		} else {
			return database.GetItems(db, acc)
		}
	}

	// get all the desired items for this Account
	items := make([]*database.Item, 0)
	itemList, itemsErr := fetch(db, acc)
	if itemsErr != nil {
		http.Error(w, itemsErr.Error(), http.StatusInternalServerError)
		return
	}
	for _, item := range itemList {
		items = append(items, item)
	}

	// actions
	actions := make([]*Action, 0)
	// commerce options
	for _, vendor := range database.GetAllVendors(db) {
		actions = append(actions, &Action{Link: fmt.Sprintf("/buy%s/", vendor.VendorId), Icon: "fa fa-shopping-cart", Action: fmt.Sprintf("Buy from %s", vendor.DisplayName)})
	}
	if acc.Email != database.ANONYMOUS_EMAIL {
		actions = append(actions, &Action{Link: "/email/", Icon: "fa fa-envelope", Action: "Email to me"})
	}
	if favorites {
		actions = append(actions, &Action{Link: "/unfavorite/", Icon: "fa fa-star-o", Action: "Remove from favorites"})
	} else {
		actions = append(actions, &Action{Link: "/favorite/", Icon: "fa fa-star", Action: "Add to favorites"})
	}
	actions = append(actions, &Action{Link: "/delete/", Icon: "fa fa-trash", Action: "Delete"})

	// define the page title
	var titleBuffer bytes.Buffer
	if favorites {
		titleBuffer.WriteString("Favorite")
	} else {
		titleBuffer.WriteString("Scanned")
	}
	titleBuffer.WriteString(" Item")
	if len(itemList) != 1 {
		titleBuffer.WriteString("s")
	}

	p := &Page{Title: titleBuffer.String(),
		ShowItems: true,
		Scanned:   !favorites,
		ActiveTab: &ActiveTab{Scanned: !favorites, Favorites: favorites, ShowTabs: true},
		Actions:   actions,
		Items:     items}

	renderTemplate(w, p)
}

// deleteItem attempts to lookup and remove the Item for the Account and
// Item.Id combination, returning a bool on success/fail, and the db lookup
// error (if any)
func deleteItem(db *sqlite3.Conn, acc *database.Account, id int64) (bool, error) {
	result := false

	item, itemErr := database.GetSingleItem(db, acc, id)
	if itemErr == nil {
		if item.Id == id {
			item.Delete(db)
			result = true
		}
	}

	return result, itemErr
}

// processItems fetches all the Items for the given Account, and the compares
// them to the id list posted from the form. All the matches get applied
// the given function: delete, favorite, unfavorite, etc.
func processItems(w http.ResponseWriter, r *http.Request, dbCoords database.ConnCoordinates, fn func(*database.Item, *sqlite3.Conn), successTarget string) {
	// attempt to connect to the db
	db, err := database.InitializeDB(dbCoords)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer db.Close()

	// get the Account for this request
	acc, accErr := database.GetDesignatedAccount(db)
	if accErr != nil {
		http.Error(w, accErr.Error(), http.StatusInternalServerError)
		return
	}

	// get all the Items for this Account
	// and store them in a map by their Id
	items, itemsErr := database.GetItems(db, acc)
	if itemsErr != nil {
		http.Error(w, itemsErr.Error(), http.StatusInternalServerError)
		return
	}
	accountItems := make(map[int64]*database.Item)
	for _, item := range items {
		accountItems[item.Id] = item
	}

	// get the list of item ids from the POST values
	// and apply the processing function
	if "POST" == r.Method {
		r.ParseForm()
		if idVals, exists := r.PostForm["item"]; exists {
			for _, idString := range idVals {
				id, idErr := strconv.ParseInt(idString, 10, 64)
				if idErr == nil {
					if accountItem, ok := accountItems[id]; ok {
						fn(accountItem, db)
					}
				}
			}
		}
	}

	// finally, return home, to the scanned items list
	http.Redirect(w, r, successTarget, http.StatusFound)
}

/* HTML Response Functions (via templates) */

func renderTemplate(w http.ResponseWriter, p *Page) {
	if TEMPLATES_INITIALIZED {
		ITEM_TEMPLATES.Execute(w, p)
	}
}

// InitializeTemplates confirms the given folder string leads to the html
// template files, otherwise templates.Must() will complain
func InitializeTemplates(folder string) {
	ITEM_TEMPLATES = template.Must(template.ParseFiles(TEMPLATE_LIST(folder, ITEM_TEMPLATE_FILES)...))
	TEMPLATES_INITIALIZED = true
}

// ScannedItems returns all the products scanned, favorited or not, barcode
// lookup successful or not
func ScannedItems(w http.ResponseWriter, r *http.Request, db database.ConnCoordinates) {
	getItems(w, r, db, false)
}

// FavoritedItems returns all the products scanned and favorited by this
// Account
func FavoritedItems(w http.ResponseWriter, r *http.Request, db database.ConnCoordinates) {
	getItems(w, r, db, true)
}

// DeleteItems accepts a form post of one or more Item.Id values, and
// attempts to remove them from the client db. Unless it hits a critical
// error, it returns home, to the list of scanned items
func DeleteItems(w http.ResponseWriter, r *http.Request, dbCoords database.ConnCoordinates) {
	del := func(i *database.Item, db *sqlite3.Conn) {
		i.Delete(db)
	}
	processItems(w, r, dbCoords, del, "/")
}

// FavoriteItems accepts a form post of one or more Item.Id values, and
// attempts to change their status in the client db to 'favorite'
func FavoriteItems(w http.ResponseWriter, r *http.Request, dbCoords database.ConnCoordinates) {
	fav := func(i *database.Item, db *sqlite3.Conn) {
		i.Favorite(db)
	}
	processItems(w, r, dbCoords, fav, "/favorites/")
}

// UnfavoriteItems accepts a form post of one or more Item.Id values,
// and attempts to change their status in the client db to not 'favorite'
func UnfavoriteItems(w http.ResponseWriter, r *http.Request, dbCoords database.ConnCoordinates) {
	fav := func(i *database.Item, db *sqlite3.Conn) {
		i.Unfavorite(db)
	}
	processItems(w, r, dbCoords, fav, "/favorites/")
}

/* Ajax Response Functions (as strings via MakeHandler) */

// RemoveSingleItem looks up the single item represented by the itemId form
// post variable, and attempts to delete it, if it exists. The reply is a
// jsonified string, passed back to MakeHandler() to be coupled with the
// right mime type
func RemoveSingleItem(r *http.Request, dbCoords database.ConnCoordinates) string {
	// prepare the ajax reply object
	ack := AjaxAck{Message: "", Error: ""}

	// attempt to connect to the db
	db, err := database.InitializeDB(dbCoords)
	if err != nil {
		ack.Error = err.Error()
	}
	defer db.Close()

	if err == nil {
		// get the Account for this request
		acc, accErr := database.GetDesignatedAccount(db)
		if accErr != nil {
			ack.Error = accErr.Error()
		}

		// find the specific Item to remove
		// get the item id from the POST values
		if "POST" == r.Method {
			r.ParseForm()
			if idVal, exists := r.PostForm["itemId"]; exists {
				if len(idVal) > 0 {
					id, idErr := strconv.ParseInt(idVal[0], 10, 64)
					if idErr != nil {
						ack.Error = idErr.Error()
					} else {
						deleteSuccess, deleteErr := deleteItem(db, acc, id)
						if deleteSuccess {
							ack.Message = "Ok"
						} else {
							if deleteErr != nil {
								ack.Error = deleteErr.Error()
							} else {
								ack.Error = "No such item"
							}
						}
					}
				} else {
					ack.Error = "Missing item id"
				}
			} else {
				ack.Error = "Bad POST data"
			}
		} else {
			ack.Error = "Bad Request"
		}
	}

	// convert the ajax reply object to json
	ackObj, ackObjErr := json.Marshal(ack)
	if ackObjErr != nil {
		return ackObjErr.Error()
	}
	return string(ackObj)
}
