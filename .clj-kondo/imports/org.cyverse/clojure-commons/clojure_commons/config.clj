(ns clojure-commons.config)

(defn record-missing-prop
  "Records a property that is missing.  Instead of failing on the first missing parameter, we log
   the missing parameter, mark the configuration as invalid and keep going so that we can log as
   many configuration errors as possible in one run.

   Parameters:
       prop-name    - the name of the property.
       config-valid - a ref containing a validity flag."
  [_prop-name _config-valid])

(defn get-required-prop
  "Gets a required property from a set of properties.

   Parameters:
       props        - a ref containing the properties.
       prop-name    - the name of the property.
       config-valid - a ref containing a validity flag."
  [props prop-name config-valid]
  (let [value (get @props prop-name "")]
    (when (empty? value)
      (record-missing-prop prop-name config-valid))
    value))

(defn get-optional-prop
  "Gets an optional property from a set of properties.

   Parameters:
       props        - a ref containing the properties.
       prop-name    - the name of the property.
       config-valid - a ref containing a validity flag.
       default      - the default property value."
  ([props prop-name _config-valid]
     (get @props prop-name ""))
  ([props prop-name _config-valid default]
     (get @props prop-name default)))

(defn- wrap-extraction-fn
  "Places a property extraction function in an appropriate wrapper, depending on whether or not
   the property is flagged. This function depends on the fact that property validation is done
   in the extraction function itself. For flagged properties that are disabled, the extraction
   function is not called when the value is retrieved. Therefore the validation for disabled
   properties is skipped because the extraction function isn't called.

   Parameters:
       extraction-fn - the actual extraction function.
       flag-props    - the feature flag properties determining if the property is relevant."
  [extraction-fn flag-props]
  (if (empty? flag-props)
    `(memoize ~extraction-fn)
    `(memoize (fn [] (when (some identity ~(vec flag-props)) (~extraction-fn))))))

(defn define-property
  "Defines a property. This is a helper function that performs common tasks required by all of the
   defprop macros.

   Parameters:
       sym           - the symbol to define.
       desc          - a brief description of the property.
       configs       - a ref containing the list of config settings.
       flag-props    - the feature flag properties determining if the property is relevant.
       extraction-fn - the function used to extract the property value."
  [sym desc configs flag-props extraction-fn]
  (let [wrapped-extraction-fn (wrap-extraction-fn extraction-fn flag-props)]
    `(conj ~configs (def ~sym ~desc ~wrapped-extraction-fn))))

(defn define-required-property
  "Defines a required property. This is a helper function that performs common tasks required by
   the macros for required properties.

   Parameters:
       sym           - the symbol to define.
       desc          - a brief description of the property.
       props         - a ref containing the properties.
       config-valid  - a ref containing the validity flag.
       configs       - a ref containing the list of config settings.
       flag-props    - the feature flag properties determining if the property is relevant.
       prop-name     - the name of the property.
       extraction-fn - the function used to extract the property value."
  [sym desc [props config-valid configs flag-props] prop-name extraction-fn]
  (define-property sym desc configs flag-props
    `(fn [] (~extraction-fn ~props ~prop-name ~config-valid))))

(defn define-optional-property
  "Defines an optional property. This is a helper function that performs common tasks required by
   the macros for optional properties.

   Parameters:
       sym           - the symbol to define.
       desc          - a brief description of the property.
       props         - a ref containing the properties.
       config-valid  - a ref containing the validity flag.
       configs       - a ref containing the list of config settings.
       flag-props    - the feature flag properties determining if the property is relevant.
       prop-name     - the name of the property.
       extraction-fn - the function used to extract the property value.
       default-value - the default value for the property."
  [sym desc [props config-valid configs flag-props] prop-name extraction-fn default-value]
  (define-property sym desc configs flag-props
    `(fn [] (~extraction-fn ~props ~prop-name ~config-valid ~default-value))))

(defmacro defprop-str
  "defines a required string property.

   Parameters:
       sym          - the symbol to define.
       desc         - a brief description of the property.
       props        - a ref containing the properties.
       config-valid - a ref containing a validity flag.
       configs      - a ref containing the list of config settings.
       flag-props   - the feature flag properties determining if the property is relevant.
       prop-name    - the name of the property."
  [sym desc [props config-valid configs & flag-props] prop-name]
  (define-required-property
    sym desc [props config-valid configs flag-props] prop-name get-required-prop))

(defmacro defprop-optstr
  "Defines an optional string property.

   Parameters:
       sym          - the symbol to define.
       desc         - a brief description of the property.
       props        - a ref containing the properties.
       config-valid - a ref containing a validity flag.
       configs      - a ref containing the list of config settings.
       flag-props   - the feature flag properties determining if the property is relevant.
       prop-name    - the name of the property.
       default      - the default value."
  ([sym desc [props config-valid configs & flag-props] prop-name]
     (define-optional-property
       sym desc [props config-valid configs flag-props] prop-name get-optional-prop ""))
  ([sym desc [props config-valid configs & flag-props] prop-name default]
     (define-optional-property
       sym desc [props config-valid configs flag-props] prop-name get-optional-prop default)))
