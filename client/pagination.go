package client

// Page is what every cursor-paginated endpoint returns. Declared structurally
// rather than against a generated type so one iterator serves them all.
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// Iterate walks every item across pages, calling fetch only when the previous
// page has been consumed.
//
// Lazy on purpose: taking the first item costs one request rather than walking
// the whole history. The cursor never reaches the caller -- it is opaque, and a
// client that constructs one breaks the moment the sort key changes.
//
// yield returns false to stop early.
func Iterate[T any](
	fetch func(cursor string) (Page[T], error),
	yield func(item T) bool,
) error {
	cursor := ""
	for {
		page, err := fetch(cursor)
		if err != nil {
			return err
		}
		for _, item := range page.Items {
			if !yield(item) {
				return nil
			}
		}
		// `hasMore` is deliberately not consulted: a page can report more while
		// giving no cursor to reach it, and looping on that spins forever.
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

// Collect gathers up to max items (0 = all). A convenience over Iterate for
// callers who want a slice and know the result is bounded.
func Collect[T any](fetch func(cursor string) (Page[T], error), max int) ([]T, error) {
	var out []T
	err := Iterate(fetch, func(item T) bool {
		out = append(out, item)
		return max <= 0 || len(out) < max
	})
	return out, err
}
