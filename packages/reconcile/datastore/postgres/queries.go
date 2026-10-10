package postgres

const itemColumns = `kind, id, priority, abandoned, failures, last_error, pending`

// Enqueue preserves ownership and diagnostics. The sequence retains events
// that arrive between claim and completion.
const enqueueQuery = `
 INSERT INTO factory_reconcile_resources (namespace, kind, id, priority)
 VALUES ($1, $2, $3, $4)
 ON CONFLICT (namespace, kind, id) DO UPDATE
 SET priority = GREATEST(factory_reconcile_resources.priority, EXCLUDED.priority),
     pending = true,
     wake_sequence = factory_reconcile_resources.wake_sequence + 1,
     due_at = clock_timestamp()
`

const getItemQuery = `
 SELECT ` + itemColumns + `
 FROM factory_reconcile_resources
 WHERE namespace = $1 AND kind = $2 AND id = $3
`

// Only placeholder positions are formatted. Kind values remain parameters.
const claimKeyQuery = `
 WITH candidate AS (
  SELECT namespace, kind, id
  FROM factory_reconcile_resources
  WHERE namespace = $1
    AND kind IN (%s)
    AND pending
    AND due_at <= clock_timestamp()
    AND (not_before IS NULL OR not_before <= clock_timestamp())
    AND (lease_until IS NULL OR lease_until <= clock_timestamp())
  ORDER BY priority DESC, due_at, kind, id
  FOR UPDATE SKIP LOCKED
  LIMIT 1
 )
 UPDATE factory_reconcile_resources AS resource
 SET abandoned = resource.abandoned + CASE WHEN resource.lease_until IS NOT NULL THEN 1 ELSE 0 END,
     fence = resource.fence + 1,
     lease_until = clock_timestamp() + $2::bigint * interval '1 microsecond'
 FROM candidate
 WHERE resource.namespace = candidate.namespace
   AND resource.kind = candidate.kind
   AND resource.id = candidate.id
 RETURNING resource.kind, resource.id, resource.priority, resource.abandoned, resource.failures,
           resource.fence, resource.wake_sequence
`

const lockClaimQuery = `
 SELECT 1
 FROM factory_reconcile_resources
 WHERE namespace = $1 AND kind = $2 AND id = $3 AND fence = $4
 FOR UPDATE
`

// A newer enqueue overrides this completion's requested schedule.
const commitItemQuery = `
 UPDATE factory_reconcile_resources
 SET priority = CASE WHEN $6 = '' AND wake_sequence = $5 AND NOT $7 THEN 0 ELSE priority END,
     not_before = CASE WHEN $9 THEN clock_timestamp() + $8::bigint * interval '1 microsecond' ELSE NULL END,
     abandoned = CASE WHEN $6 = '' THEN 0 ELSE abandoned END,
     failures = CASE WHEN $6 = '' THEN 0 ELSE failures + 1 END,
     last_error = $6,
     pending = (wake_sequence <> $5 OR $7),
     due_at = CASE
      WHEN wake_sequence <> $5 THEN clock_timestamp()
      ELSE clock_timestamp() + $8::bigint * interval '1 microsecond'
     END,
     lease_until = NULL
 WHERE namespace = $1 AND kind = $2 AND id = $3
   AND fence = $4
   AND lease_until IS NOT NULL
`

const renewClaimQuery = `
 UPDATE factory_reconcile_resources
 SET lease_until = GREATEST(lease_until,
     clock_timestamp() + $5::bigint * interval '1 microsecond')
 WHERE namespace = $1 AND kind = $2 AND id = $3
   AND fence = $4 AND lease_until IS NOT NULL
`
const releaseClaimQuery = `
 UPDATE factory_reconcile_resources
 SET lease_until = NULL, pending = true, due_at = clock_timestamp()
 WHERE namespace = $1 AND kind = $2 AND id = $3
   AND fence = $4 AND lease_until IS NOT NULL
`
const redriveKeyQuery = `
 UPDATE factory_reconcile_resources
 SET not_before = NULL, abandoned = 0, failures = 0, last_error = '', pending = true,
     due_at = clock_timestamp(), wake_sequence = wake_sequence + 1
 WHERE namespace = $1 AND kind = $2 AND id = $3 AND lease_until IS NULL
`
