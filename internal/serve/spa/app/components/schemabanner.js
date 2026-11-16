// schemabanner.js - the page-wide "a newer lmm upgraded the database" notice
// (issue 648).
//
// Another, newer lmm migrated the database after this server opened it, so
// the server now refuses every /api/v1 request (middleware.go's schemaGate)
// with the refusal's code in the envelope's details. api.js notes the first
// such refusal from whichever request met it, and this banner shows from
// then on. It has no dismiss button: nothing on this page works until the
// server is restarted onto the newer binary, and restarting this same one
// would only refuse again - so the remedy stays in view. role="alert"
// announces it once, when it appears.

import { html, useEffect, useState } from "../render.js";
import { onSchemaRefusal, schemaRefusalNow } from "../api.js";

/** SchemaBanner renders the restart notice once any request has met the
 * schema refusal, or nothing. */
export function SchemaBanner() {
  const [refusal, setRefusal] = useState(schemaRefusalNow);

  useEffect(() => {
    // A refusal noted between the first render and this effect.
    setRefusal(schemaRefusalNow());
    return onSchemaRefusal(setRefusal);
  }, []);

  if (!refusal) return null;
  return html`
    <section
      class="release-banner release-banner--alert"
      role="alert"
      aria-label="lmm restart required"
      data-testid="schema-banner"
    >
      <p class="release-banner__text">
        A newer lmm upgraded the database (schema version ${refusal.version};
        this lmm reads up to ${refusal.supported}). This web UI can no longer
        read or change it: restart <code>lmm serve</code> on the newer binary
        (if it runs as a service, run <code>lmm service install</code> from the
        newer lmm).
      </p>
    </section>
  `;
}
