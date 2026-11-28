// The few TypeBox builders the ask_user tool uses, for tests outside pi (pi supplies the real typebox).
export const Type = {
  Object: (properties) => ({ type: 'object', properties }),
  String: (o = {}) => ({ type: 'string', ...o }),
  Boolean: () => ({ type: 'boolean' }),
  Integer: (o = {}) => ({ type: 'integer', ...o }),
  Number: (o = {}) => ({ type: 'number', ...o }),
  Array: (items, o = {}) => ({ type: 'array', items, ...o }),
  Optional: (s) => ({ ...s, optional: true }),
}
