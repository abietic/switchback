package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	artifacthttp "github.com/abietic/switchback/internal/artifacttrace/http"
)

type ContentReader struct {
	Pool *pgxpool.Pool
}

func (reader ContentReader) ReadArtifactContent(
	ctx context.Context,
	viewer artifacthttp.FixedViewer,
	artifactID string,
) (artifacthttp.ArtifactContent, error) {
	var content artifacthttp.ArtifactContent
	err := reader.Pool.QueryRow(ctx, `
SELECT artifact_id,artifact_type,object_key,object_checksum,size_bytes
FROM artifact_refs
WHERE artifact_id=$1 AND tenant_id=$2 AND user_id=$3
  AND artifact_ref->>'availability'='online'`, artifactID, viewer.TenantID, viewer.UserID).Scan(
		&content.ArtifactID, &content.Type, &content.ObjectKey, &content.Checksum, &content.SizeBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifacthttp.ArtifactContent{}, errors.New("Artifact is unavailable")
	}
	return content, err
}

var _ artifacthttp.ContentReader = ContentReader{}
