import { escapeHTML, formatNumber } from "../utils.js";

export function renderCollectionPagination(collectionName, pagination, currentItemCount) {
  const previousPageAvailable = (pagination?.previousCursors?.length || 0) > 0;
  const nextPageAvailable = Boolean(pagination?.hasMore && pagination?.nextCursor);
  if (!previousPageAvailable && !nextPageAvailable) {
    return "";
  }

  const currentPage = (pagination?.previousCursors?.length || 0) + 1;
  const totalCount = Number(pagination?.totalCount || 0);
  const totalLabel = totalCount > 0 ? ` · 共 ${formatNumber(totalCount)} 条` : "";
  return `
    <footer class="collection-pagination" aria-label="列表分页">
      <span>第 ${escapeHTML(formatNumber(currentPage))} 页 · 本页 ${escapeHTML(formatNumber(currentItemCount))} 条${escapeHTML(totalLabel)}</span>
      <div class="collection-pagination-actions">
        <button class="button button-secondary button-sm" type="button" data-action="change-list-page" data-list="${escapeHTML(collectionName)}" data-direction="previous" ${previousPageAvailable ? "" : "disabled"}>上一页</button>
        <button class="button button-secondary button-sm" type="button" data-action="change-list-page" data-list="${escapeHTML(collectionName)}" data-direction="next" ${nextPageAvailable ? "" : "disabled"}>下一页</button>
      </div>
    </footer>
  `;
}
