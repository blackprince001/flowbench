class TemplateRef(str):
  """A ``{{ ref }}`` template placeholder that is also a plain ``str``.

  Subclassing ``str`` means a TemplateRef renders as its template text
  everywhere ordinary Python would stringify it -- f-strings, dict
  literals, ``json.dumps`` -- with no custom serialization code. ``ref``
  and ``builder`` stay attached only for the object identity case (a bare
  ``ctx.vars["x"]`` expression passed straight into ``expect()`` or
  assigned back into ``ctx.vars``); any string operation applied to a
  TemplateRef degrades it to plain text, which is the desired final form.
  """

  def __new__(cls, ref, builder=None):
    obj = str.__new__(cls, "{{ " + ref + " }}")
    obj.ref = ref
    obj._builder = builder
    return obj


def env(name):
  """A ``{{ env.NAME }}`` reference to a process environment variable.

  ``ctx.env[name]`` covers the same ground inside a step function; this is
  the spelling for credentials declared outside one -- on a Flow or a step
  decorator, where there is no ``ctx`` yet.
  """
  return TemplateRef(f"env.{name}")
