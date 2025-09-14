-- name: CreateLink :execlastid
INSERT INTO links (url, commentary, title, image_url) VALUES (?, ?, ?, ?);

-- name: GetLink :one
SELECT id, url, commentary, title, image_url FROM links WHERE id = ?;

-- name: GetLinksByURL :many
SELECT id, url, commentary, title, image_url FROM links WHERE url = ?;

-- name: ListLinks :many
SELECT id, url, commentary, title, image_url FROM links ORDER BY id DESC;

-- name: DeleteLink :exec
DELETE FROM links WHERE id = ?;

-- name: UpdateLink :exec
UPDATE links SET url = ?, commentary = ?, title = ?, image_url = ? WHERE id = ?;
