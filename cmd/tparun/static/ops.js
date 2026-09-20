(() => {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const toBigInt = value => {
    try { return BigInt(String(value)); } catch (_) { return null; }
  };
  const formatBytes = value => {
    const n = toBigInt(value);
    if (n === null) return null;
    if (n < 0n) return n.toString() + ' B';
    let unit = 0;
    let divisor = 1n;
    while (unit < units.length - 1 && n >= divisor * 1000n) { divisor *= 1000n; unit++; }
    if (unit === 0) return n.toString() + ' B';
    const whole = n / divisor;
    let hundredths = ((n % divisor) * 100n + divisor / 2n) / divisor;
    let roundedWhole = whole;
    if (hundredths === 100n) { roundedWhole++; hundredths = 0n; }
    const decimals = hundredths.toString().padStart(2, '0');
    return roundedWhole.toString() + '.' + decimals + ' ' + units[unit];
  };
  const formatPercent = (used, limit) => {
    const u = toBigInt(used), l = toBigInt(limit);
    if (u === null || l === null || l <= 0n) return '';
    const tenths = (u * 1000n + l / 2n) / l;
    const whole = tenths / 10n;
    return tenths % 10n === 0n ? whole + '%' : whole + '.' + (tenths % 10n) + '%';
  };
  document.querySelectorAll('.resource-usage').forEach(e => {
    const used = e.dataset.usedBytes, limit = e.dataset.limitBytes;
    const usedText = formatBytes(used);
    if (usedText === null) return;
    if (e.dataset.limitState === 'none' || e.dataset.limitState === 'mixed') {
      e.textContent = usedText + ' / no applicable limit';
      return;
    }
    if (e.dataset.limitState !== 'finite') return;
    const limitText = formatBytes(limit);
    if (limitText === null) return;
    e.textContent = usedText + ' / ' + limitText;
    const percent = formatPercent(used, limit);
    if (percent) e.textContent += ' (' + percent + ')';
  });
  document.querySelectorAll('.js-bytes').forEach(e => { const text = formatBytes(e.dataset.bytes); if (text !== null) e.textContent = text; });
})();
