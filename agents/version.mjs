// Shared precedence for installed-agent probes and their validated drift reports.
export function cmpVersion(a, b) {
  const parse = (value) => {
    const match = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/.exec(value);
    if (!match) return null;
    return {
      core: [BigInt(match[1]), BigInt(match[2]), BigInt(match[3])],
      prerelease: match[4]?.split(".") ?? null,
    };
  };
  const left = parse(a);
  const right = parse(b);
  if (!left || !right) return Number.NaN;
  for (let index = 0; index < left.core.length; index++) {
    if (left.core[index] !== right.core[index]) return left.core[index] < right.core[index] ? -1 : 1;
  }
  // SemVer precedence: a release outranks its prerelease; prerelease identifiers
  // compare numeric-before-text, then by length when every shared identifier ties.
  if (left.prerelease === null || right.prerelease === null) {
    if (left.prerelease === right.prerelease) return 0;
    return left.prerelease === null ? 1 : -1;
  }
  for (let index = 0; index < Math.max(left.prerelease.length, right.prerelease.length); index++) {
    const x = left.prerelease[index];
    const y = right.prerelease[index];
    if (x === undefined) return -1;
    if (y === undefined) return 1;
    const xNumeric = /^\d+$/.test(x);
    const yNumeric = /^\d+$/.test(y);
    if (xNumeric && yNumeric) {
      const xNumber = BigInt(x);
      const yNumber = BigInt(y);
      if (xNumber !== yNumber) return xNumber < yNumber ? -1 : 1;
    } else if (xNumeric !== yNumeric) {
      return xNumeric ? -1 : 1;
    } else if (x !== y) {
      return x < y ? -1 : 1;
    }
  }
  return 0;
}
