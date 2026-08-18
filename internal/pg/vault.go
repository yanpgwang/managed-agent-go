package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

var _ app.VaultRepository = (*VaultRepository)(nil)

type VaultRepository struct{ store *Store }

func NewVaultRepository(store *Store) *VaultRepository {
	return &VaultRepository{store: store}
}

func (r *VaultRepository) CreateVault(ctx context.Context, item domain.Vault) (domain.Vault, error) {
	workspaceID, err := r.store.workspaceForWrite(ctx)
	if err != nil {
		return domain.Vault{}, err
	}
	normalizeVaultTimes(&item)
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return domain.Vault{}, err
	}
	created, err := scanVault(r.store.pool.QueryRow(ctx, `
INSERT INTO vaults (id, workspace_id, display_name, metadata, created_at, updated_at, archived_at)
VALUES ($1, $2, $3, $4, $5, $6, NULL)
RETURNING id, display_name, metadata, created_at, updated_at, archived_at`,
		item.ID, workspaceID, item.DisplayName, metadata, item.CreatedAt, item.UpdatedAt,
	))
	if isUniqueViolation(err) {
		return domain.Vault{}, domain.Conflict("vault already exists")
	}
	return created, err
}

func (r *VaultRepository) GetVault(ctx context.Context, id string) (domain.Vault, error) {
	workspaceID, scoped, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return domain.Vault{}, err
	}
	query := `
SELECT id, display_name, metadata, created_at, updated_at, archived_at
FROM vaults WHERE id = $1`
	args := []any{id}
	if scoped {
		query += ` AND workspace_id = $2`
		args = append(args, workspaceID)
	}
	item, err := scanVault(r.store.pool.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Vault{}, domain.NotFound("vault not found")
	}
	return item, err
}

func (r *VaultRepository) UpdateVault(ctx context.Context, id string, patch app.VaultUpdateInput, clock domain.Clock) (domain.Vault, error) {
	workspaceID, scoped, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return domain.Vault{}, err
	}
	var updated domain.Vault
	err = r.store.withPGXTx(ctx, func(tx pgx.Tx, _ *pgstore.Queries) error {
		query := `
SELECT id, display_name, metadata, created_at, updated_at, archived_at
FROM vaults WHERE id = $1`
		args := []any{id}
		if scoped {
			query += ` AND workspace_id = $2`
			args = append(args, workspaceID)
		}
		current, err := scanVault(tx.QueryRow(ctx, query+` FOR UPDATE`, args...))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("vault not found")
		}
		if err != nil {
			return err
		}
		if current.ArchivedAt != nil {
			return domain.Validation("archived vaults are read-only")
		}
		displayName := current.DisplayName
		metadata := clonePGStringMap(current.Metadata)
		if patch.DisplayName.Present && patch.DisplayName.Value != nil {
			displayName = *patch.DisplayName.Value
		}
		if patch.Metadata.Present {
			if patch.Metadata.Value == nil {
				metadata = map[string]string{}
			} else {
				for key, value := range *patch.Metadata.Value {
					if value == nil {
						delete(metadata, key)
					} else {
						metadata[key] = *value
					}
				}
			}
		}
		if len(metadata) > 16 {
			return domain.Validation("metadata must contain at most 16 entries")
		}
		if displayName == current.DisplayName && equalStringMap(metadata, current.Metadata) {
			updated = current
			return nil
		}
		metadataJSON, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		now := clock.Now().UTC().Truncate(time.Microsecond)
		updateQuery := `
UPDATE vaults SET display_name = $2, metadata = $3, updated_at = $4
WHERE id = $1`
		updateArgs := []any{id, displayName, metadataJSON, now}
		if scoped {
			updateQuery += ` AND workspace_id = $5`
			updateArgs = append(updateArgs, workspaceID)
		}
		updated, err = scanVault(tx.QueryRow(ctx, updateQuery+`
RETURNING id, display_name, metadata, created_at, updated_at, archived_at`, updateArgs...))
		return err
	})
	return updated, err
}

func (r *VaultRepository) ListVaults(ctx context.Context, query app.VaultListQuery) (app.VaultListPage, error) {
	args := make([]any, 0, 4)
	where := []string{"true"}
	workspaceID, scoped, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return app.VaultListPage{}, err
	}
	if scoped {
		args = append(args, workspaceID)
		where = append(where, fmt.Sprintf("workspace_id = $%d", len(args)))
	}
	if !query.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if query.After != nil {
		args = append(args, query.After.CreatedAt.UTC(), query.After.ID)
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, query.Limit+1)
	rows, err := r.store.pool.Query(ctx, fmt.Sprintf(`
SELECT id, display_name, metadata, created_at, updated_at, archived_at
FROM vaults WHERE %s
ORDER BY created_at DESC, id DESC LIMIT $%d`, strings.Join(where, " AND "), len(args)), args...)
	if err != nil {
		return app.VaultListPage{}, err
	}
	defer rows.Close()
	items := make([]domain.Vault, 0, query.Limit+1)
	for rows.Next() {
		item, scanErr := scanVault(rows)
		if scanErr != nil {
			return app.VaultListPage{}, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return app.VaultListPage{}, err
	}
	page := app.VaultListPage{Vaults: items}
	if len(items) > query.Limit {
		page.Vaults = items[:query.Limit]
		page.HasNext = true
	}
	return page, nil
}

func (r *VaultRepository) ArchiveVault(ctx context.Context, id string, clock domain.Clock) (domain.Vault, error) {
	workspaceID, scoped, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return domain.Vault{}, err
	}
	var archived domain.Vault
	err = r.store.withPGXTx(ctx, func(tx pgx.Tx, _ *pgstore.Queries) error {
		query := `
SELECT id, display_name, metadata, created_at, updated_at, archived_at
FROM vaults WHERE id = $1`
		args := []any{id}
		if scoped {
			query += ` AND workspace_id = $2`
			args = append(args, workspaceID)
		}
		current, err := scanVault(tx.QueryRow(ctx, query+` FOR UPDATE`, args...))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("vault not found")
		}
		if err != nil {
			return err
		}
		if current.ArchivedAt != nil {
			archived = current
			return nil
		}
		now := clock.Now().UTC().Truncate(time.Microsecond)
		if _, err := tx.Exec(ctx, `
UPDATE vault_credentials
SET archived_at = $2, updated_at = $2, version = version + 1,
    secret_version = NULL, secret_algorithm = NULL, secret_key_id = NULL,
    secret_nonce = NULL, secret_ciphertext = NULL
WHERE vault_id = $1 AND archived_at IS NULL`, id, now); err != nil {
			return err
		}
		updateQuery := `UPDATE vaults SET archived_at = $2, updated_at = $2 WHERE id = $1`
		updateArgs := []any{id, now}
		if scoped {
			updateQuery += ` AND workspace_id = $3`
			updateArgs = append(updateArgs, workspaceID)
		}
		archived, err = scanVault(tx.QueryRow(ctx, updateQuery+`
RETURNING id, display_name, metadata, created_at, updated_at, archived_at`, updateArgs...))
		return err
	})
	return archived, err
}

func (r *VaultRepository) DeleteVault(ctx context.Context, id string) error {
	workspaceID, scoped, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return err
	}
	query := `DELETE FROM vaults WHERE id = $1`
	args := []any{id}
	if scoped {
		query += ` AND workspace_id = $2`
		args = append(args, workspaceID)
	}
	tag, err := r.store.pool.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.NotFound("vault not found")
	}
	return nil
}

func (r *VaultRepository) CreateCredential(ctx context.Context, item domain.VaultCredential, maxCredentials int) (domain.VaultCredential, error) {
	if _, err := r.GetVault(ctx, item.VaultID); err != nil {
		return domain.VaultCredential{}, err
	}
	normalizeCredentialTimes(&item)
	if item.SecretEnvelope == nil {
		return domain.VaultCredential{}, errors.New("credential secret envelope is required")
	}
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return domain.VaultCredential{}, err
	}
	publicAuth, err := json.Marshal(item.Auth)
	if err != nil {
		return domain.VaultCredential{}, err
	}
	var created domain.VaultCredential
	err = r.store.withPGXTx(ctx, func(tx pgx.Tx, _ *pgstore.Queries) error {
		var archivedAt *time.Time
		if err := tx.QueryRow(ctx, `SELECT archived_at FROM vaults WHERE id = $1 FOR UPDATE`, item.VaultID).Scan(&archivedAt); errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("vault not found")
		} else if err != nil {
			return err
		}
		if archivedAt != nil {
			return domain.Validation("archived vaults are read-only")
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM vault_credentials WHERE vault_id = $1`, item.VaultID).Scan(&count); err != nil {
			return err
		}
		if count >= maxCredentials {
			return domain.Validation("vault already contains 20 credentials")
		}
		rows, err := tx.Query(ctx, `
SELECT credential_key FROM vault_credentials
WHERE vault_id = $1 AND archived_at IS NULL`, item.VaultID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var existingKey string
			if err := rows.Scan(&existingKey); err != nil {
				rows.Close()
				return err
			}
			existingCanonical, existingErr := app.CanonicalMCPServerURL(existingKey)
			newCanonical, newErr := app.CanonicalMCPServerURL(item.CredentialKey)
			if existingErr == nil && newErr == nil && existingCanonical == newCanonical {
				rows.Close()
				return domain.Conflict("an active credential for this MCP server already exists in the vault")
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		envelope := item.SecretEnvelope
		created, err = scanVaultCredential(tx.QueryRow(ctx, `
INSERT INTO vault_credentials (
    id, vault_id, display_name, metadata, auth_type, credential_key, public_auth,
    secret_version, secret_algorithm, secret_key_id, secret_nonce, secret_ciphertext,
    version, created_at, updated_at, archived_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,NULL)
RETURNING id, vault_id, display_name, metadata, auth_type, credential_key, public_auth,
          secret_version, secret_algorithm, secret_key_id, secret_nonce, secret_ciphertext,
          version, created_at, updated_at, archived_at`,
			item.ID, item.VaultID, item.DisplayName, metadata, item.Auth.Type, item.CredentialKey, publicAuth,
			envelope.Version, envelope.Algorithm, envelope.KeyID, envelope.Nonce, envelope.Ciphertext,
			item.Version, item.CreatedAt, item.UpdatedAt,
		))
		return err
	})
	if isUniqueViolation(err) {
		return domain.VaultCredential{}, domain.Conflict("an active credential for this MCP server already exists in the vault")
	}
	return created, err
}

func (r *VaultRepository) ResolveSessionCredentials(
	ctx context.Context,
	sessionID string,
) ([]domain.VaultCredential, error) {
	workspaceID, scoped, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return nil, err
	}
	query := credentialSelect + `
JOIN session_vaults sv ON sv.vault_id = vault_credentials.vault_id
JOIN sessions s ON s.id = sv.session_id
WHERE sv.session_id = $1 AND vault_credentials.archived_at IS NULL`
	args := []any{sessionID}
	if scoped {
		query += ` AND s.workspace_id = $2`
		args = append(args, workspaceID)
	}
	rows, err := r.store.pool.Query(ctx, query+`
ORDER BY sv.position ASC, vault_credentials.id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.VaultCredential
	for rows.Next() {
		item, err := scanVaultCredential(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *VaultRepository) GetCredential(ctx context.Context, vaultID, credentialID string) (domain.VaultCredential, error) {
	if _, err := r.GetVault(ctx, vaultID); err != nil {
		return domain.VaultCredential{}, err
	}
	item, err := scanVaultCredential(r.store.pool.QueryRow(ctx, credentialSelect+`
WHERE vault_id = $1 AND id = $2`, vaultID, credentialID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VaultCredential{}, domain.NotFound("credential not found")
	}
	return item, err
}

func (r *VaultRepository) UpdateCredential(ctx context.Context, vaultID, credentialID string, update func(domain.VaultCredential) (domain.VaultCredential, bool, error)) (domain.VaultCredential, error) {
	if _, err := r.GetVault(ctx, vaultID); err != nil {
		return domain.VaultCredential{}, err
	}
	var updated domain.VaultCredential
	err := r.store.withPGXTx(ctx, func(tx pgx.Tx, _ *pgstore.Queries) error {
		current, err := scanVaultCredential(tx.QueryRow(ctx, credentialSelect+`
WHERE vault_id = $1 AND id = $2 FOR UPDATE`, vaultID, credentialID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("credential not found")
		}
		if err != nil {
			return err
		}
		next, changed, err := update(current)
		if err != nil {
			return err
		}
		if !changed {
			updated = current
			return nil
		}
		if next.ID != current.ID || next.VaultID != current.VaultID || next.CredentialKey != current.CredentialKey || next.Auth.Type != current.Auth.Type || next.Auth.MCPServerURL != current.Auth.MCPServerURL || next.Version != current.Version+1 || next.SecretEnvelope == nil {
			return errors.New("credential update violated immutable repository fields")
		}
		metadata, err := json.Marshal(next.Metadata)
		if err != nil {
			return err
		}
		publicAuth, err := json.Marshal(next.Auth)
		if err != nil {
			return err
		}
		envelope := next.SecretEnvelope
		updated, err = scanVaultCredential(tx.QueryRow(ctx, `
UPDATE vault_credentials SET
    display_name = $3, metadata = $4, public_auth = $5,
    secret_version = $6, secret_algorithm = $7, secret_key_id = $8,
    secret_nonce = $9, secret_ciphertext = $10, version = $11, updated_at = $12
WHERE vault_id = $1 AND id = $2
RETURNING id, vault_id, display_name, metadata, auth_type, credential_key, public_auth,
          secret_version, secret_algorithm, secret_key_id, secret_nonce, secret_ciphertext,
          version, created_at, updated_at, archived_at`,
			vaultID, credentialID, next.DisplayName, metadata, publicAuth,
			envelope.Version, envelope.Algorithm, envelope.KeyID, envelope.Nonce,
			envelope.Ciphertext, next.Version, next.UpdatedAt.UTC().Truncate(time.Microsecond),
		))
		return err
	})
	return updated, err
}

func (r *VaultRepository) ListCredentials(ctx context.Context, vaultID string, query app.CredentialListQuery) (app.CredentialListPage, error) {
	if _, err := r.GetVault(ctx, vaultID); err != nil {
		return app.CredentialListPage{}, err
	}
	args := []any{vaultID}
	where := []string{"vault_id = $1"}
	if !query.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if query.After != nil {
		args = append(args, query.After.CreatedAt.UTC(), query.After.ID)
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, query.Limit+1)
	rows, err := r.store.pool.Query(ctx, credentialSelect+fmt.Sprintf(`
WHERE %s ORDER BY created_at DESC, id DESC LIMIT $%d`, strings.Join(where, " AND "), len(args)), args...)
	if err != nil {
		return app.CredentialListPage{}, err
	}
	defer rows.Close()
	items := make([]domain.VaultCredential, 0, query.Limit+1)
	for rows.Next() {
		item, scanErr := scanVaultCredential(rows)
		if scanErr != nil {
			return app.CredentialListPage{}, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return app.CredentialListPage{}, err
	}
	page := app.CredentialListPage{Credentials: items}
	if len(items) > query.Limit {
		page.Credentials = items[:query.Limit]
		page.HasNext = true
	}
	return page, nil
}

func (r *VaultRepository) ArchiveCredential(ctx context.Context, vaultID, credentialID string, clock domain.Clock) (domain.VaultCredential, error) {
	if _, err := r.GetVault(ctx, vaultID); err != nil {
		return domain.VaultCredential{}, err
	}
	now := clock.Now().UTC().Truncate(time.Microsecond)
	item, err := scanVaultCredential(r.store.pool.QueryRow(ctx, `
UPDATE vault_credentials SET
    archived_at = COALESCE(archived_at, $3),
    updated_at = CASE WHEN archived_at IS NULL THEN $3 ELSE updated_at END,
    version = CASE WHEN archived_at IS NULL THEN version + 1 ELSE version END,
    secret_version = NULL, secret_algorithm = NULL, secret_key_id = NULL,
    secret_nonce = NULL, secret_ciphertext = NULL
WHERE vault_id = $1 AND id = $2
RETURNING id, vault_id, display_name, metadata, auth_type, credential_key, public_auth,
          secret_version, secret_algorithm, secret_key_id, secret_nonce, secret_ciphertext,
          version, created_at, updated_at, archived_at`, vaultID, credentialID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VaultCredential{}, domain.NotFound("credential not found")
	}
	return item, err
}

func (r *VaultRepository) DeleteCredential(ctx context.Context, vaultID, credentialID string) error {
	if _, err := r.GetVault(ctx, vaultID); err != nil {
		return err
	}
	tag, err := r.store.pool.Exec(ctx, `DELETE FROM vault_credentials WHERE vault_id = $1 AND id = $2`, vaultID, credentialID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.NotFound("credential not found")
	}
	return nil
}

const credentialSelect = `
SELECT vault_credentials.id, vault_credentials.vault_id,
       vault_credentials.display_name, vault_credentials.metadata,
       vault_credentials.auth_type, vault_credentials.credential_key,
       vault_credentials.public_auth, vault_credentials.secret_version,
       vault_credentials.secret_algorithm, vault_credentials.secret_key_id,
       vault_credentials.secret_nonce, vault_credentials.secret_ciphertext,
       vault_credentials.version, vault_credentials.created_at,
       vault_credentials.updated_at, vault_credentials.archived_at
FROM vault_credentials `

type vaultScanner interface{ Scan(...any) error }

func scanVault(row vaultScanner) (domain.Vault, error) {
	var item domain.Vault
	var metadata []byte
	err := row.Scan(&item.ID, &item.DisplayName, &metadata, &item.CreatedAt, &item.UpdatedAt, &item.ArchivedAt)
	if err != nil {
		return domain.Vault{}, err
	}
	if err := json.Unmarshal(metadata, &item.Metadata); err != nil {
		return domain.Vault{}, err
	}
	if item.Metadata == nil {
		item.Metadata = map[string]string{}
	}
	return item, nil
}

func scanVaultCredential(row vaultScanner) (domain.VaultCredential, error) {
	var item domain.VaultCredential
	var authType string
	var metadata, publicAuth []byte
	var secretVersion *int
	var secretAlgorithm, secretKeyID *string
	var secretNonce, secretCiphertext []byte
	err := row.Scan(
		&item.ID, &item.VaultID, &item.DisplayName, &metadata, &authType,
		&item.CredentialKey, &publicAuth, &secretVersion, &secretAlgorithm, &secretKeyID,
		&secretNonce, &secretCiphertext, &item.Version, &item.CreatedAt, &item.UpdatedAt,
		&item.ArchivedAt,
	)
	if err != nil {
		return domain.VaultCredential{}, err
	}
	if err := json.Unmarshal(metadata, &item.Metadata); err != nil {
		return domain.VaultCredential{}, err
	}
	if err := json.Unmarshal(publicAuth, &item.Auth); err != nil {
		return domain.VaultCredential{}, err
	}
	if item.Auth.Type == "" || item.Auth.Type != authType {
		return domain.VaultCredential{}, errors.New("credential public auth type does not match stored auth type")
	}
	if secretVersion != nil && secretAlgorithm != nil && secretKeyID != nil {
		item.SecretEnvelope = &domain.SecretEnvelope{
			Version: *secretVersion, Algorithm: *secretAlgorithm, KeyID: *secretKeyID,
			Nonce: append([]byte(nil), secretNonce...), Ciphertext: append([]byte(nil), secretCiphertext...),
		}
	}
	if item.Metadata == nil {
		item.Metadata = map[string]string{}
	}
	return item, nil
}

func normalizeVaultTimes(item *domain.Vault) {
	item.CreatedAt = item.CreatedAt.UTC().Truncate(time.Microsecond)
	item.UpdatedAt = item.UpdatedAt.UTC().Truncate(time.Microsecond)
}

func normalizeCredentialTimes(item *domain.VaultCredential) {
	item.CreatedAt = item.CreatedAt.UTC().Truncate(time.Microsecond)
	item.UpdatedAt = item.UpdatedAt.UTC().Truncate(time.Microsecond)
}
