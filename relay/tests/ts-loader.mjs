// Rewrites a failed `./foo.js` resolution to `./foo.ts` so a test can import
// the Worker's TypeScript sources directly under Node's built-in type
// stripping. src/*.ts is written for a bundler's moduleResolution (it imports
// './encoding.js' even though the file on disk is encoding.ts); a bundler
// resolves that automatically, but plain Node does not. This loader fills
// only that one gap — it changes no source file and fires only when the `.js`
// specifier does not exist.
export async function resolve(specifier, context, nextResolve) {
	try {
		return await nextResolve(specifier, context);
	} catch (error) {
		if (specifier.endsWith('.js') && error?.code === 'ERR_MODULE_NOT_FOUND') {
			return nextResolve(`${specifier.slice(0, -3)}.ts`, context);
		}
		throw error;
	}
}
