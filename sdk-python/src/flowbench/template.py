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
