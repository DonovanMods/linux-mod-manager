package serve

import (
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// statusPageData is "/"'s template data.
type statusPageData struct {
	Title string
	Games []statusGameCard
}

// statusGameCard pairs one game's Status summary with its richer GameStatus
// detail (docs/plans/2026-08-30-serve-design.md §HTTP surface: "/" renders
// via Status and GameStatus). Detail is nil when that per-game lookup
// fails, degrading the card to its summary fields only - the same
// best-effort philosophy Status itself uses across games, extended across
// this one extra call.
type statusGameCard struct {
	Summary core.GameSummary
	Detail  *core.GameStatus
}

// handleStatus renders the status dashboard.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	report, err := s.svc.Status(ctx)
	if err != nil {
		s.renderError(w, err)
		return
	}

	data := statusPageData{Title: "Status", Games: make([]statusGameCard, 0, len(report.Games))}
	for _, summary := range report.Games {
		card := statusGameCard{Summary: summary}
		game := summary.Game
		if detail, derr := s.svc.GameStatus(ctx, &game); derr == nil {
			card.Detail = detail
		} else {
			s.log.Debug("status page: GameStatus failed", "game", summary.ID, "err", derr)
		}
		data.Games = append(data.Games, card)
	}

	s.render(w, statusTemplate, data)
}
