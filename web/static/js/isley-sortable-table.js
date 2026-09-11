/**
 * IsleySortableTable — Synchronizes clickable <th> column-header sorting
 * with a companion "Sort by" <select> dropdown, matching the pattern
 * used across the plants, strains, sensors, and activities list views.
 *
 * Usage:
 *   const sortableTable = new IsleySortableTable(sortBySelectEl, {
 *       prefix:           "pt",     // CSS class prefix, e.g. ".pt-sortable"
 *       headerToDropdown: {},       // data-sort key -> { asc, desc } dropdown option values
 *       dropdownToHeader: {},       // dropdown value prefix -> data-sort key or [keys]
 *       onChange:         null,     // callback(state) -> call your applyFilters()
 *   });
 *
 *   // Programmatic control:
 *   sortableTable.getSort();               // { key, asc } - single source of truth for applyFilters()
 *   sortableTable.resetToDefault("name-asc"); // used by "Clear filters" buttons / view switches
 *
 * Pass `null` as the first argument for header-only tables with no
 * companion dropdown (e.g. activities) - header-click sorting still works,
 * dropdown sync is just skipped.
 */
class IsleySortableTable {
    constructor(dropdownEl, opts = {}) {
        this.opts = Object.assign({
            prefix: "",
            headerToDropdown: {},
            dropdownToHeader: {},
            onChange: null,
        }, opts);

        if (!this.opts.prefix) {
            console.warn("IsleySortableTable: no `prefix` provided");
        }

        this.dropdown = dropdownEl || null;
        this.state = { key: null, asc: true };

        this._headers = document.querySelectorAll(`.${this.opts.prefix}-sortable`);
        this._bindHeaders();
        if (this.dropdown) this._bindDropdown();
    }

    /* ---- public API ---- */

    /** Single source of truth each page's applyFilters() should read. */
    getSort() {
        if (this.state.key) return { key: this.state.key, asc: this.state.asc };
        if (this.dropdown) {
            const [k, d] = this.dropdown.value.split("-");
            return { key: k, asc: d === "asc" };
        }
        return { key: null, asc: true };
    }

    /** Used by "Clear filters" buttons and view-switch handlers. */
    resetToDefault(defaultDropdownValue) {
        this.state = { key: null, asc: true };
        this._resetIcons();
        if (this.dropdown && defaultDropdownValue) {
            this.dropdown.value = defaultDropdownValue;
        }
    }

    /* ---- private ---- */

    _resetIcons() {
        document.querySelectorAll(`.${this.opts.prefix}-sortable i`).forEach(icon => {
            icon.className = "fa-solid fa-sort ms-1 text-muted";
        });
    }

    _setIcon(key, asc) {
        const th = document.querySelector(`.${this.opts.prefix}-sortable[data-sort="${key}"]`);
        const icon = th && th.querySelector("i");
        if (icon) icon.className = `fa-solid fa-sort-${asc ? "up" : "down"} ms-1`;
    }

    _bindHeaders() {
        this._headers.forEach(th => {
            th.style.cursor = "pointer";
            th.addEventListener("click", () => {
                const key = th.dataset.sort;
                if (this.state.key === key) {
                    this.state.asc = !this.state.asc;
                } else {
                    this.state.key = key;
                    this.state.asc = true;
                }

                this._resetIcons();
                this._setIcon(key, this.state.asc);

                if (this.dropdown) {
                    const mapped = this.opts.headerToDropdown[key];
                    const value = mapped && (this.state.asc ? mapped.asc : mapped.desc);
                    if (value && this.dropdown.querySelector(`option[value="${value}"]`)) {
                        this.dropdown.value = value;
                    }
                }

                if (this.opts.onChange) this.opts.onChange(this.state);
            });
        });
    }

    _bindDropdown() {
        this.dropdown.addEventListener("change", () => {
            this.state = { key: null, asc: true };
            this._resetIcons();

            const [key, dir] = this.dropdown.value.split("-");
            const asc = dir === "asc";
            const mapped = this.opts.dropdownToHeader[key];
            if (mapped) {
                (Array.isArray(mapped) ? mapped : [mapped]).forEach(k => this._setIcon(k, asc));
            }

            if (this.opts.onChange) this.opts.onChange(this.state);
        });
    }
}
