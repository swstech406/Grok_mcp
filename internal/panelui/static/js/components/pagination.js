import { escapeHTML, formatNumber } from "../utils.js";
import { COLLECTION_PAGE_SIZE_OPTIONS } from "../pagination-config.js";

export function renderCollectionPagination(collectionName, pagination, currentItemCount, options = {}) {
  const previousPageAvailable = (pagination?.previousCursors?.length || 0) > 0;
  const nextPageAvailable = Boolean(pagination?.hasMore && pagination?.nextCursor);
  const showPageSizeSelector = Boolean(options.showPageSizeSelector);
  if (!previousPageAvailable && !nextPageAvailable && !showPageSizeSelector) {
    return "";
  }

  const currentPage = (pagination?.previousCursors?.length || 0) + 1;
  const selectedPageSize = Number(pagination?.pageSize || 50);
  const totalCount = Number(pagination?.totalCount || 0);
  const totalLabel = totalCount > 0 ? ` · 共 ${formatNumber(totalCount)} 条` : "";
  return `
    <footer class="collection-pagination" aria-label="列表分页">
      <span>第 ${escapeHTML(formatNumber(currentPage))} 页 · 本页 ${escapeHTML(formatNumber(currentItemCount))} 条${escapeHTML(totalLabel)}</span>
      <div class="collection-pagination-actions">
        ${showPageSizeSelector ? `
          <label class="pagination-page-size">
            <span>每页</span>
            <select class="select-input" data-action="change-list-page-size" data-list="${escapeHTML(collectionName)}" aria-label="每页显示条数">
              ${COLLECTION_PAGE_SIZE_OPTIONS.map((pageSize) => `<option value="${pageSize}" ${selectedPageSize === pageSize ? "selected" : ""}>${pageSize} 条</option>`).join("")}
            </select>
          </label>
        ` : ""}
        <button class="button button-secondary button-sm" type="button" data-action="change-list-page" data-list="${escapeHTML(collectionName)}" data-direction="previous" ${previousPageAvailable ? "" : "disabled"}>上一页</button>
        <button class="button button-secondary button-sm" type="button" data-action="change-list-page" data-list="${escapeHTML(collectionName)}" data-direction="next" ${nextPageAvailable ? "" : "disabled"}>下一页</button>
      </div>
    </footer>
  `;
}
